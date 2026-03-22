package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/onkernel/kernel-images/server/lib/logger"
	xp "github.com/reclaimprotocol/xpath-go"
)

// ─── Match + Extract + Build in one call ───

type MatchRequest struct {
	CapturedURL     string            `json:"captured_url"`
	CapturedMethod  string            `json:"captured_method"`
	CapturedHeaders map[string]string `json:"captured_headers"`
	CapturedBody    string            `json:"captured_body,omitempty"`
	ResponseBody    string            `json:"response_body"`
	ResponseStatus  int               `json:"response_status,omitempty"`
	Pattern         PatternConfig     `json:"pattern"`
	Parameters      map[string]string `json:"parameters,omitempty"` // Existing known params
	GeoLocation     string            `json:"geo_location,omitempty"`
}

type PatternConfig struct {
	URL                string              `json:"url"`
	Method             string              `json:"method"`
	ResponseMatches    []ResponseMatchConf `json:"responseMatches,omitempty"`
	ResponseRedactions []RedactionConf     `json:"responseRedactions,omitempty"`
	BodySniff          *BodySniffConf      `json:"bodySniff,omitempty"`
	ResponseVariables  []string            `json:"responseVariables,omitempty"`
}

type ResponseMatchConf struct {
	Value  string `json:"value"`
	Type   string `json:"type"` // "contains" or "regex"
	Invert bool   `json:"invert,omitempty"`
}

type RedactionConf struct {
	XPath    string `json:"xPath,omitempty"`
	JSONPath string `json:"jsonPath,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

type BodySniffConf struct {
	Enabled  bool   `json:"enabled"`
	Template string `json:"template,omitempty"`
}

type MatchResponse struct {
	Matched            bool              `json:"matched"`
	ExtractedParams    map[string]string `json:"extracted_params,omitempty"`
	ProviderParamsJSON string            `json:"provider_params_json,omitempty"` // Ready for /reclaim/prove
	Error              string            `json:"error,omitempty"`
}

// Default public headers (same as browser-events claim-builder)
var defaultPublicHeaders = map[string]bool{
	"user-agent": true, "accept": true, "accept-language": true, "accept-encoding": true,
	"sec-fetch-mode": true, "sec-fetch-site": true, "sec-fetch-user": true, "sec-fetch-dest": true,
	"origin": true, "x-requested-with": true, "sec-ch-ua": true, "sec-ch-ua-mobile": true,
	"sec-ch-ua-platform": true, "content-type": true, "content-length": true,
}

// HandleMatch does extraction, matching, and builds provider_params_json in one call.
// POST /browser-events/match
func (s *ApiService) HandleMatch(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context())

	var req MatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, MatchResponse{Error: "invalid request body"})
		return
	}

	// Step 1: Extract variables from response using XPath/JSONPath + regex
	log.Info("match request", "url", req.CapturedURL, "method", req.CapturedMethod,
		"redactions", fmt.Sprintf("%+v", req.Pattern.ResponseRedactions),
		"variables", fmt.Sprintf("%v", req.Pattern.ResponseVariables),
		"responseBodyLen", len(req.ResponseBody))
	extractedParams := extractVariables(req.ResponseBody, req.Pattern.ResponseRedactions, req.Pattern.ResponseVariables, log)

	// Merge with provided parameters (extracted overrides provided)
	allParams := make(map[string]string)
	for k, v := range req.Parameters {
		allParams[k] = v
	}
	for k, v := range extractedParams {
		allParams[k] = v
	}

	// Extract body template params
	if req.Pattern.BodySniff != nil && req.Pattern.BodySniff.Enabled && req.Pattern.BodySniff.Template != "" {
		bodyParams := extractBodyTemplateParams(req.CapturedBody, req.Pattern.BodySniff.Template)
		for k, v := range bodyParams {
			allParams[k] = v
		}
	}

	// Step 2: Check responseMatches using extracted params
	for _, match := range req.Pattern.ResponseMatches {
		matchValue := substituteTemplateVars(match.Value, allParams)

		// If unresolved {{vars}} remain, can't match
		if strings.Contains(matchValue, "{{") {
			respondJSON(w, http.StatusOK, MatchResponse{
				Matched:         false,
				ExtractedParams: extractedParams,
				Error:           fmt.Sprintf("unresolved template vars in: %s", matchValue),
			})
			return
		}

		var matched bool
		if match.Type == "contains" {
			matched = strings.Contains(req.ResponseBody, matchValue)
		} else if match.Type == "regex" {
			re, err := regexp.Compile("(?s)" + matchValue)
			if err == nil {
				matched = re.MatchString(req.ResponseBody)
			}
		}
		if match.Invert {
			matched = !matched
		}
		if !matched {
			respondJSON(w, http.StatusOK, MatchResponse{
				Matched:         false,
				ExtractedParams: extractedParams,
			})
			return
		}
	}

	// Step 3: Build provider_params_json
	providerParams := buildProviderParamsGo(req, allParams)
	providerParamsJSON, err := json.Marshal(providerParams)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, MatchResponse{Error: "failed to marshal provider params"})
		return
	}

	log.Info("match successful", "url", req.CapturedURL, "extracted", fmt.Sprintf("%v", extractedParams))

	respondJSON(w, http.StatusOK, MatchResponse{
		Matched:            true,
		ExtractedParams:    extractedParams,
		ProviderParamsJSON: string(providerParamsJSON),
	})
}

// extractVariables uses the Go xpath-go library (same as TEE) for XPath extraction.
func extractVariables(responseBody string, redactions []RedactionConf, varNames []string, log interface{ Warn(string, ...any) }) map[string]string {
	extracted := make(map[string]string)

	for i, redaction := range redactions {
		element := responseBody

		log.Warn("extracting", "index", i, "xPath", redaction.XPath, "jsonPath", redaction.JSONPath, "regex", redaction.Regex, "regexBytes", fmt.Sprintf("%q", redaction.Regex), "elementLen", len(element), "elementFirst100", element[:min(len(element), 100)])

		// XPath extraction
		if redaction.XPath != "" {
			needsFullHTML := redaction.Regex != "" && strings.Contains(redaction.Regex, "<")
			contentsOnly := !needsFullHTML

			results, err := xp.QueryWithOptions(redaction.XPath, element, xp.Options{
				IncludeLocation: true,
				OutputFormat:    "nodes",
				ContentsOnly:    contentsOnly,
			})
			if err != nil || len(results) == 0 {
				continue
			}
			element = results[0].Value
		}

		// JSONPath extraction
		jsonPathExtracted := false
		if redaction.JSONPath != "" && redaction.XPath == "" {
			// Simple $.fieldName support
			re := regexp.MustCompile(`^\$\.(\w+)$`)
			if m := re.FindStringSubmatch(redaction.JSONPath); m != nil {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(element), &parsed); err == nil {
					if val, ok := parsed[m[1]]; ok {
						element = fmt.Sprintf("%v", val)
						jsonPathExtracted = true
					}
				}
			}
		}

		// If JSONPath already extracted the value, use it directly
		// (regex like "userName":"(.*)" is meant for the full response, not the extracted value)
		if jsonPathExtracted {
			varName := ""
			if i < len(varNames) {
				varName = varNames[i]
			}
			if varName != "" {
				extracted[varName] = element
			}
			log.Warn("jsonPath extracted directly", "index", i, "var", varName, "value", element)
			continue
		}

		// Regex extraction (for XPath-extracted elements or full response body)
		if redaction.Regex != "" {
			re, err := regexp.Compile("(?s)" + redaction.Regex)
			if err != nil {
				log.Warn("regex compile failed", "regex", redaction.Regex, "err", err)
				continue
			}
			match := re.FindStringSubmatch(element)
			log.Warn("regex result", "regex", redaction.Regex, "matchLen", len(match), "firstGroup", func() string { if len(match) > 1 { return match[1][:min(len(match[1]), 100)] }; return "<no match>" }())
			if match != nil && len(match) > 1 {
				varName := ""
				if i < len(varNames) {
					varName = varNames[i]
				}
				if varName != "" {
					extracted[varName] = match[1]
				}

				// Also check named groups
				for _, name := range re.SubexpNames() {
					if name != "" {
						idx := re.SubexpIndex(name)
						if idx > 0 && idx < len(match) && match[idx] != "" {
							extracted[name] = match[idx]
						}
					}
				}
			}
		}
	}

	return extracted
}

func extractBodyTemplateParams(body string, template string) map[string]string {
	if body == "" || template == "" {
		return nil
	}

	// Find {{VAR}} placeholders
	re := regexp.MustCompile(`\{\{(\w+)\}\}`)
	vars := re.FindAllStringSubmatchIndex(template, -1)
	if len(vars) == 0 {
		return nil
	}

	// Build regex from template
	var varNames []string
	regexStr := ""
	lastEnd := 0
	for _, loc := range vars {
		// loc[0]:loc[1] = full match, loc[2]:loc[3] = group 1
		regexStr += regexp.QuoteMeta(template[lastEnd:loc[0]])
		regexStr += `([\s\S]*?)`
		varNames = append(varNames, template[loc[2]:loc[3]])
		lastEnd = loc[1]
	}
	regexStr += regexp.QuoteMeta(template[lastEnd:])

	bodyRe, err := regexp.Compile(regexStr)
	if err != nil {
		return nil
	}

	match := bodyRe.FindStringSubmatch(body)
	if match == nil {
		return nil
	}

	result := make(map[string]string)
	for i, name := range varNames {
		if i+1 < len(match) {
			result[name] = match[i+1]
		}
	}
	return result
}

func substituteTemplateVars(template string, params map[string]string) string {
	result := template
	for k, v := range params {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

func buildProviderParamsGo(req MatchRequest, allParams map[string]string) map[string]interface{} {
	// Separate public vs secret params
	publicParams := make(map[string]string)
	secretParamValues := make(map[string]string)
	for k, v := range allParams {
		if strings.Contains(strings.ToUpper(k), "SECRET") {
			secretParamValues[k] = v
		} else {
			publicParams[k] = v
		}
	}

	// Add DYNAMIC_GEO
	if req.GeoLocation != "" {
		publicParams["DYNAMIC_GEO"] = req.GeoLocation
	}

	// Separate headers
	publicHeaders := make(map[string]string)
	secretHeaders := make(map[string]string)
	cookieStr := ""
	for k, v := range req.CapturedHeaders {
		lower := strings.ToLower(k)
		if lower == "cookie" {
			cookieStr = v
		} else if defaultPublicHeaders[lower] {
			publicHeaders[toTitleCaseHeader(lower)] = v
		} else {
			secretHeaders[toTitleCaseHeader(lower)] = v
		}
	}

	// Body: use bodySniff template or raw body
	body := ""
	if req.Pattern.BodySniff != nil && req.Pattern.BodySniff.Enabled && req.Pattern.BodySniff.Template != "" {
		body = req.Pattern.BodySniff.Template
	} else if strings.ToUpper(req.CapturedMethod) == "POST" && req.CapturedBody != "" {
		body = req.CapturedBody
	}

	// Clean responseRedactions — remove empty fields
	var cleanRedactions []map[string]interface{}
	for _, r := range req.Pattern.ResponseRedactions {
		cleaned := make(map[string]interface{})
		if r.XPath != "" {
			cleaned["xPath"] = r.XPath
		}
		if r.JSONPath != "" {
			cleaned["jsonPath"] = r.JSONPath
		}
		if r.Regex != "" {
			cleaned["regex"] = r.Regex
		}
		if r.Hash != "" {
			cleaned["hash"] = r.Hash
		}
		cleanRedactions = append(cleanRedactions, cleaned)
	}

	// Clean responseMatches
	var cleanMatches []map[string]interface{}
	for _, m := range req.Pattern.ResponseMatches {
		cleaned := map[string]interface{}{
			"value": m.Value,
			"type":  m.Type,
		}
		if m.Invert {
			cleaned["invert"] = true
		}
		cleanMatches = append(cleanMatches, cleaned)
	}

	geoStr := ""
	if req.GeoLocation != "" {
		geoStr = "{{DYNAMIC_GEO}}"
	}

	return map[string]interface{}{
		"name": "http",
		"params": map[string]interface{}{
			"url":                req.Pattern.URL,
			"method":             req.CapturedMethod,
			"body":               body,
			"headers":            publicHeaders,
			"responseMatches":    cleanMatches,
			"responseRedactions": cleanRedactions,
			"paramValues":        publicParams,
			"geoLocation":        geoStr,
			"writeRedactionMode": "zk",
		},
		"secretParams": map[string]interface{}{
			"headers":     secretHeaders,
			"cookieStr":   cookieStr,
			"paramValues": secretParamValues,
		},
	}
}

func toTitleCaseHeader(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

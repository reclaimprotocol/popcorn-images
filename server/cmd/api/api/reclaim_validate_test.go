package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateExtractionWithTEEXPathText(t *testing.T) {
	resp := validateExtractionWithTEE(reclaimValidateExtractionRequest{
		ResponseBody:  `<div id="global"><ul><li>John Doe</li></ul></div>`,
		ExpectedValue: "John Doe",
		XPath:         `//div[@id="global"]/ul/li/text()[1]`,
		Regex:         `(?<fullName>.+)`,
	})

	require.True(t, resp.Valid)
	require.True(t, resp.TEEValid)
	require.NotNil(t, resp.ExtractedValue)
	require.Equal(t, "John Doe", *resp.ExtractedValue)
	require.Nil(t, resp.Error)
}

func TestValidateExtractionWithTEERequiresSelector(t *testing.T) {
	resp := validateExtractionWithTEE(reclaimValidateExtractionRequest{
		ResponseBody:  `{"name":"John Doe"}`,
		ExpectedValue: "John Doe",
	})

	require.False(t, resp.Valid)
	require.NotNil(t, resp.Error)
	require.Contains(t, *resp.Error, "Expected either xPath, jsonPath or regex")
}

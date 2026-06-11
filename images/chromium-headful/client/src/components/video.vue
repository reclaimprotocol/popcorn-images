<template>
  <div ref="component" class="video">
    <div ref="player" class="player">
      <div ref="container" :class="['player-container', mobileViewportActive ? 'mobile-viewport' : '']">
        <video ref="video" playsinline />
        <div class="emotes">
          <template v-for="(emote, index) in emotes">
            <neko-emote :id="index" :key="index" />
          </template>
        </div>
        <textarea
          ref="overlay"
          class="overlay"
          spellcheck="false"
          tabindex="0"
          data-gramm="false"
          :style="{ pointerEvents: hosting ? 'auto' : 'none' }"
          @click.stop.prevent
          @contextmenu.stop.prevent
          @wheel.stop.prevent="onWheel"
          @mousemove.stop.prevent="onMouseMove"
          @mousedown.stop.prevent="onMouseDown"
          @mouseup.stop.prevent="onMouseUp"
          @mouseenter.stop.prevent="onMouseEnter"
          @mouseleave.stop.prevent="onMouseLeave"
          @touchmove.stop.prevent="onTouchHandler"
          @touchstart.stop.prevent="onTouchHandler"
          @touchend.stop.prevent="onTouchHandler"
          @touchcancel.stop.prevent="onTouchHandler"
          @paste.stop.prevent="onPaste"
          @focus="onOverlayFocus"
        />
        <!-- Hidden proxy <input> for mobile IME capture. Styled to be visually
             invisible but IME-recognizable (fontSize:16px prevents iOS zoom;
             real <input> with text inputMode is required for Samsung Keyboard
             and SwiftKey, which reject hidden cross-iframe textareas).
             Positioned fixed off-screen when keyboard is inactive; moves to
             viewport-center on activation so platform IMEs can attach. -->
        <input
          ref="proxyInput"
          type="text"
          class="mobile-proxy-input"
          :style="proxyInputStyle"
          autocomplete="off"
          autocorrect="off"
          autocapitalize="off"
          spellcheck="false"
          inputmode="text"
          tabindex="0"
          @beforeinput="onProxyBeforeInput"
          @input="onProxyInput"
          @keydown="onProxyKeyDown"
          @blur="onProxyBlur"
          @compositionstart="onCompositionStart"
          @compositionend="onCompositionEnd"
        />
        <!-- KERNEL
        <div v-if="!playing && playable" class="player-overlay" @click.stop.prevent="playAndUnmute">
          <i class="fas fa-play-circle" />
        </div>
        <div v-else-if="mutedOverlay && muted" class="player-overlay" @click.stop.prevent="unmute">
          <i class="fas fa-volume-up" />
        </div>
-->
        <div ref="aspect" class="player-aspect" />
        <!-- ?debug=rects: paints translucent red boxes over every cached
             input rect so we can visually verify what the hit-test sees.
             pointer-events:none so it never absorbs taps. -->
        <div v-if="debugRects" class="rect-debug-overlay" aria-hidden="true">
          <div
            v-for="(r, i) in debugRectStyles"
            :key="i"
            class="rect-debug-box"
            :style="r"
          />
          <div class="rect-debug-status">
            rects: {{ inputRectsCache.length }} ready={{ inputRectsReady }}<br>
            vp: {{ cdpFocusCache && cdpFocusCache.viewportWidth || '?' }}×{{ cdpFocusCache && cdpFocusCache.viewportHeight || '?' }}<br>
            kbd: {{ keyboardActive }} last: {{ lastHitResult }}
          </div>
        </div>
        <!-- Close button for fullscreen popups (window.open). Popups have no
             browser chrome in kiosk mode, so this is the only way to dismiss
             one. Compact icon, top-right, above every overlay. -->
        <button
          v-if="hosting && popupOpen"
          class="popup-close-btn"
          @click.stop.prevent="closePopup"
          aria-label="Close popup"
        >
          <i class="fas fa-times" />
        </button>
        <!-- JS dialog (alert/confirm/prompt) overlay. The native dialog draws
             at the OS-window level outside the emulated crop and is suppressed
             by Page.enable, so we render our own here, in the viewport space,
             and answer it over CDP via /cdp/dialog-respond. -->
        <div v-if="hosting && pendingDialog" class="js-dialog-overlay">
          <div class="js-dialog">
            <div class="js-dialog-msg">{{ pendingDialog.message }}</div>
            <input
              v-if="pendingDialog.kind === 'prompt'"
              ref="dialogInput"
              v-model="dialogPromptText"
              class="js-dialog-input"
              type="text"
              @keydown.enter.stop.prevent="respondDialog(true)"
            />
            <div class="js-dialog-actions">
              <button
                v-if="pendingDialog.kind !== 'alert'"
                class="js-dialog-btn cancel"
                @click.stop.prevent="respondDialog(false)"
              >Cancel</button>
              <button class="js-dialog-btn ok" @click.stop.prevent="respondDialog(true)">OK</button>
            </div>
          </div>
        </div>
      </div>
      <ul v-if="!fullscreen && !hideControls" class="video-menu top">
        <!-- KERNEL: disable fullscreen and resolution controls
        <li><i @click.stop.prevent="requestFullscreen" class="fas fa-expand"></i></li>
        <li v-if="admin"><i @click.stop.prevent="openResolution" class="fas fa-desktop"></i></li>
        -->
        <li v-if="!controlLocked && !implicitHosting" :class="extraControls || 'extra-control'">
          <i
            :class="[
              hosted && !hosting ? 'disabled' : '',
              !hosted && !hosting ? 'faded' : '',
              'fas',
              'fa-computer-mouse',
            ]"
            @click.stop.prevent="toggleControl"
          />
        </li>
      </ul>
      <ul v-if="!fullscreen && !hideControls" class="video-menu bottom">
        <li v-if="hosting && (!clipboard_read_available || !clipboard_write_available)">
          <!-- KERNEL: disable clipboard controls
          <i @click.stop.prevent="openClipboard" class="fas fa-clipboard"></i>
          -->
        </li>
        <li>
          <!-- KERNEL: disable pip
          <i
            v-if="pip_available"
            @click.stop.prevent="requestPictureInPicture"
            v-tooltip="{ content: 'Picture-in-Picture', placement: 'left', offset: 5, boundariesElement: 'body' }"
            class="fas fa-external-link-alt"
          />
          -->
        </li>
        <li
          v-if="hosting && is_touch_device"
          :class="extraControls || 'extra-control'"
          @click.stop.prevent="openMobileKeyboard"
        >
          <i class="fas fa-keyboard" />
        </li>
        <li
          v-if="hosting && is_touch_device"
          :class="extraControls || 'extra-control'"
          @click.stop.prevent="toggleMagnify"
        >
          <i :class="['fas', isMagnified ? 'fa-compress' : 'fa-search-plus']" />
        </li>
      </ul>
      <neko-resolution ref="resolution" v-if="admin" />
      <neko-clipboard ref="clipboard" v-if="hosting && (!clipboard_read_available || !clipboard_write_available)" />
    </div>
  </div>
</template>

<style lang="scss" scoped>
  .video {
    width: 100%;
    height: 100%;

    .player {
      position: absolute;
      display: flex;
      justify-content: center;
      align-items: center;
      background: #000;


      .video-menu {
        position: absolute;
        right: 20px;

        &.top {
          top: 15px;
        }

        &.bottom {
          bottom: 15px;
        }

        li {
          margin: 0 0 10px 0;

          i {
            width: 30px;
            height: 30px;
            background: rgba($color: #fff, $alpha: 0.2);
            border-radius: 5px;
            line-height: 30px;
            font-size: 16px;
            text-align: center;
            color: rgba($color: #fff, $alpha: 0.6);
            cursor: pointer;

            &.faded {
              color: rgba($color: $text-normal, $alpha: 0.4);
            }

            &.disabled {
              color: rgba($color: $style-error, $alpha: 0.4);
            }
          }

          /* usually extra controls are only shown on mobile */
          &.extra-control {
            display: none;
          }
          @media (max-width: 768px) {
            &.extra-control {
              display: block;
            }
          }

          &:last-child {
            margin: 0;
          }
        }
      }

      .player-container {
        position: relative;
        width: 100%;
        max-width: calc(16 / 9 * 100vh);
        overflow: hidden;

        video {
          position: absolute;
          top: 0;
          bottom: 0;
          width: 100%;
          height: 100%;
          display: flex;
          background: #000;

          &::-webkit-media-controls {
            display: none !important;
          }

        }

        .player-overlay,
        .emotes {
          position: absolute;
          top: 0;
          bottom: 0;
          width: 100%;
          height: 100%;
          overflow: hidden;
        }

        .player-overlay {
          background: rgba($color: #000, $alpha: 0.2);
          display: flex;
          justify-content: center;
          align-items: center;
          cursor: pointer;

          i::before {
            font-size: 120px;
            text-align: center;
          }

          &.hidden {
            display: none;
          }
        }

        .overlay {
          position: absolute;
          top: 0;
          bottom: 0;
          width: 100%;
          height: 100%;
          cursor: default;
          outline: 0;
          border: 0;
          color: transparent;
          caret-color: transparent;
          background: transparent;
          resize: none;
          touch-action: none;
        }

        .player-aspect {
          display: block;
          padding-bottom: 56.25%;
        }

        /* Mobile viewport magnify: fill the screen and crop the 1920×1080
           stream to show only the top-left emulated area at 1:1 pixels.
           Pairs with the `./cdp-magnify.sh` script which tells chromium to
           render the page into that same top-left rect.
           Uses 100dvh (dynamic viewport height) to avoid iOS Safari's
           100vh bug where it includes the area behind the address bar. */
        &.mobile-viewport {
          max-width: 100vw !important;
          width: 100vw !important;
          height: 100vh !important;
          height: 100dvh !important;

          video {
            object-fit: none;
            object-position: 0 0;
            width: 100vw !important;
            height: 100vh !important;
            height: 100dvh !important;
          }

          .overlay {
            width: 100vw !important;
            height: 100vh !important;
            height: 100dvh !important;
          }

          .player-aspect {
            display: none !important;
          }
        }

        /* Mobile IME proxy input. Visually invisible but real enough for
           Samsung Keyboard / SwiftKey heuristics to attach (real <input>,
           opacity:0.01 not opacity:0, 16px font to prevent iOS zoom). */
        .mobile-proxy-input {
          opacity: 0.01;
          width: 40px;
          height: 20px;
          font-size: 16px;
          border: 0;
          outline: 0;
          padding: 0;
          margin: 0;
          background: transparent;
          color: transparent;
          caret-color: transparent;
          z-index: 9999;
        }

        /* ?debug=rects overlay. Fixed-position boxes painted at the same
           screen coords the hit-test uses, so visual position == what the
           system believes is an input. */
        .rect-debug-overlay {
          position: fixed;
          inset: 0;
          pointer-events: none;
          z-index: 9998;
        }
        .rect-debug-box {
          position: fixed;
          border: 2px solid rgba(255, 0, 64, 0.85);
          background: rgba(255, 0, 64, 0.12);
          box-sizing: border-box;
          pointer-events: none;
        }
        /* Close button for chromeless fullscreen popups. Compact circular icon
           pinned to the top-right corner, above every other overlay so it's
           reachable while a popup covers the stream. */
        .popup-close-btn {
          position: fixed;
          top: 10px;
          right: 10px;
          z-index: 10000;
          display: flex;
          align-items: center;
          justify-content: center;
          width: 34px;
          height: 34px;
          padding: 0;
          color: #fff;
          background: rgba($color: #000, $alpha: 0.6);
          border: 1px solid rgba($color: #fff, $alpha: 0.25);
          border-radius: 50%;
          cursor: pointer;

          i {
            font-size: 15px;
          }
        }

        /* JS dialog overlay — a native-looking modal rendered in the viewport
           space (the real native dialog can't show under emulation). Above all
           other overlays; the scrim absorbs taps so nothing leaks to the page. */
        .js-dialog-overlay {
          position: fixed;
          inset: 0;
          z-index: 10001;
          display: flex;
          align-items: center;
          justify-content: center;
          background: rgba($color: #000, $alpha: 0.4);
        }
        .js-dialog {
          width: min(80vw, 320px);
          max-height: 70vh;
          overflow-y: auto;
          background: #fff;
          color: #1a1a1a;
          border-radius: 12px;
          padding: 18px 16px 12px;
          box-shadow: 0 8px 30px rgba(0, 0, 0, 0.35);
        }
        .js-dialog-msg {
          white-space: pre-wrap;
          word-break: break-word;
          margin-bottom: 12px;
          font-size: 14px;
          line-height: 1.4;
        }
        .js-dialog-input {
          width: 100%;
          box-sizing: border-box;
          font-size: 16px; /* 16px prevents iOS focus-zoom */
          padding: 8px;
          border: 1px solid #ccc;
          border-radius: 6px;
          margin-bottom: 12px;
          color: #1a1a1a;
        }
        .js-dialog-actions {
          display: flex;
          justify-content: flex-end;
          gap: 8px;
        }
        .js-dialog-btn {
          padding: 8px 16px;
          border: 0;
          border-radius: 6px;
          font-size: 14px;
          cursor: pointer;

          &.cancel {
            background: #eee;
            color: #333;
          }
          &.ok {
            background: #2563eb;
            color: #fff;
          }
        }

        .rect-debug-status {
          position: fixed;
          top: 4px;
          left: 4px;
          padding: 4px 6px;
          font: 11px/1.3 monospace;
          background: rgba(0, 0, 0, 0.75);
          color: #0f0;
          border: 1px solid rgba(0, 255, 0, 0.4);
          border-radius: 3px;
          pointer-events: none;
          z-index: 9999;
          white-space: nowrap;
        }
      }
    }
  }
</style>

<script lang="ts">
  import { Component, Ref, Watch, Vue, Prop } from 'vue-property-decorator'
  import ResizeObserver from 'resize-observer-polyfill'
  import { elementRequestFullscreen, onFullscreenChange, isFullscreen, lockKeyboard, unlockKeyboard } from '~/utils'

  import Emote from './emote.vue'
  import Resolution from './resolution.vue'
  import Clipboard from './clipboard.vue'

  // @ts-ignore
  import GuacamoleKeyboard from '~/utils/guacamole-keyboard.ts'

  const WHEEL_LINE_HEIGHT = 19

  // Remote pixels scrolled per wheel "tick" we emit — one tick == one
  // XTestFakeButtonEvent == ~one wheel notch ≈ 120px in Chromium (same constant
  // the desktop onWheel path normalizes against). Touch scroll divides finger
  // travel by this so a swipe moves the page ~1:1 with the finger (native feel)
  // instead of ~20× too fast. Lower = faster scroll. Tunable.
  const TOUCH_SCROLL_PX_PER_TICK = 120

  // Multiplier on CDP pixel-wheel scroll distance. 1.0 = content tracks the
  // finger exactly (1:1). We run >1 because CDP mouseWheel has no fling/inertia
  // the way native touch does — so a 1:1 swipe feels sluggish (it stops dead on
  // release instead of coasting). ~2 makes a swipe travel a believable distance.
  // Raise = faster scroll, lower = closer to 1:1. Tune to taste.
  const TOUCH_SCROLL_GAIN = 2.0

  // Scroll backpressure: if more than this many bytes are queued unsent on the
  // input data channel, stop emitting wheel events and let them coalesce in the
  // accumulator until the channel drains. Each input frame is ~7 bytes, so ~256
  // bounds the scroll backlog to a few hundred ms on a congested link — enough
  // to absorb bursts on a good network (where the buffer drains to ~0 between
  // frames) without piling up lag on a bad one. Tunable.
  const SCROLL_MAX_BUFFERED_BYTES = 256

  interface HitTestReply {
    isInput: boolean
    tag?: string
    readonly?: boolean
    disabled?: boolean
    isEditable?: boolean
    focusKey?: string
  }

  interface InputRect { x: number; y: number; width: number; height: number }

  interface ActiveElementInfo {
    isInput: boolean
    tag: string
    type?: string
    isEditable?: boolean
    readonly?: boolean
    disabled?: boolean
    focusKey?: string
    elementTop?: number
    elementHeight?: number
    elementLeft?: number
    elementWidth?: number
    selectInfo?: {
      multiple: boolean
      rect: { x: number; y: number; width: number; height: number }
      options: Array<{ value: string; text: string; selected: boolean; disabled: boolean; groupLabel?: string }>
    }
    inputRects?: InputRect[]
    viewportWidth?: number
    viewportHeight?: number
    inputType?: string
    inputMode?: string
    autoComplete?: string
    enterKeyHint?: string
  }

  @Component({
    name: 'neko-video',
    components: {
      'neko-emote': Emote,
      'neko-resolution': Resolution,
      'neko-clipboard': Clipboard,
    },
  })
  export default class extends Vue {
    @Ref('component') readonly _component!: HTMLElement
    @Ref('container') readonly _container!: HTMLElement
    @Ref('overlay') readonly _overlay!: HTMLTextAreaElement
    @Ref('proxyInput') readonly _proxyInput!: HTMLInputElement
    @Ref('aspect') readonly _aspect!: HTMLElement
    @Ref('player') readonly _player!: HTMLElement
    @Ref('video') readonly _video!: HTMLVideoElement
    @Ref('resolution') readonly _resolution!: Resolution
    @Ref('clipboard') readonly _clipboard!: Clipboard

    // all controls are hidden (e.g. for cast mode)
    @Prop(Boolean) readonly hideControls!: boolean
    // extra controls are shown (e.g. for embed mode)
    @Prop(Boolean) readonly extraControls!: boolean

    private keyboard = GuacamoleKeyboard()
    private observer = new ResizeObserver(this.onResize.bind(this))
    private focused = false
    private fullscreen = false
    private mutedOverlay = true
    private isVideoSyncing = false

    // Magnify toggle for touch viewers. true = native 1920×1080 letterboxed
    // view (initial state — page is visible at desktop aspect with bars).
    // false = mobile-viewport CSS crop active (pairs with cdp-magnify CDP
    // emulation; toggled via the magnify button which also auto-applies the
    // CDP emulation at the viewer's actual screen size).
    isMagnified = true

    // Debounce handle for re-applying mobile emulation when the iframe/visual
    // viewport changes (orientation flip, embed resize).
    private deviceExperienceTimer: number | null = null


    // Last emulate-device body actually POSTed, to skip redundant calls when
    // onResize fires repeatedly at the same dimensions (avoids polling the
    // /cdp/emulate-device endpoint).
    private lastEmulatedKey = ''

    // Last visible height pushed to the remote while the soft keyboard is up
    // (0 = full viewport / no keyboard). Used to shrink the remote viewport so
    // the focused field scrolls above the keyboard and content stays reachable.
    private lastEmulatedVisibleHeight = 0

    // IME composition state. Only used on iOS — Android (Samsung Keyboard
    // especially) treats every word as a composition session, so suppressing
    // during composition would drop all typing. Android handles intermediate
    // composition via value-comparison in onProxyInput instead.
    private isComposing = false

    // Mobile keyboard state, ported from useMobileKeyboard.ts.
    // keyboardActive: visualViewport shrunk → soft keyboard is up.
    // keyboardOpening: between focus call and the viewport actually shrinking
    //   (suppresses the dismiss-detector during the open animation).
    // keyboardJustDismissed: 100ms grace after a dismissal so taps that follow
    //   immediately don't accidentally re-pop the keyboard.
    // allowBlur: set true around intentional blurs so onProxyBlur doesn't
    //   misclassify them as a system dismissal.
    // A popup window (window.open) is open on the remote. Pushed over the
    // input-ws. Popups render fullscreen with no browser chrome, so we show an
    // in-app close button while one is open.
    popupOpen = false

    // A JavaScript dialog (alert/confirm/prompt) is awaiting a response on the
    // remote. Pushed over the input-ws; we render our own overlay because the
    // native dialog can't be shown in the emulated viewport. dialogPromptText
    // backs the input shown for prompt().
    pendingDialog: { id: number; kind: string; message: string; defaultPrompt?: string } | null = null
    dialogPromptText = ''

    private keyboardActive = false
    private keyboardOpening = false
    private keyboardJustDismissed = false
    private allowBlur = false
    private lastViewportShrink = false

    // Per-spec lift: when keyboard is up, move the streamed video element up
    // by this many pixels so the focused remote field clears the keyboard.
    private iframeOffset = 0

    // IME accounting (ported from keyboard.ts:2920-3236).
    // lastSentValue: what we've already forwarded to the remote — diff against
    //   proxyInput.value on each input event to figure out what's new.
    // pendingBackspaceTimer: for Indic/Ridmik IMEs that fire keydown
    //   key='Unidentified' for backspace; we defer 80 ms to see if an input
    //   event follows (= it was a real char), and only fire backspace if not.
    // lastCharSent / lastPunctuationTime: feed `filterAutoSpace` so we can
    //   strip SwiftKey's auto-inserted space after punctuation.
    private lastSentValue = ''
    private pendingBackspaceTimer: number | null = null
    private lastCharSent = ''
    private lastPunctuationTime = 0

    // Mobile keyboard pops on user tap only — no background polling. The
    // touchend handler does a /cdp/active-element check (sync on iOS,
    // 150 ms async on Android) and pops the keyboard if the tap landed on
    // a focusable input. This avoids the constant /cdp/active-element
    // traffic the old auto-focus poller created, which on shit networks
    // competed with text POSTs for the same HTTPS path.

    // Cached regex test once on construction; the result doesn't change.
    // Debug overlay: enable with ?debug=rects in the URL. Renders red
    // outlines over every cached input rect so we can visually verify
    // what the hit-test "sees" — invaluable on pages where the keyboard
    // misbehaves (cache stale, wrong rects, cross-origin iframe gaps).
    get debugRects(): boolean {
      const debugParam = new URLSearchParams(window.location.search).get('debug') || ''
      return debugParam.split(',').includes('rects')
    }

    // Reactivity hook: bumped on every WS focus push so the v-for above
    // re-renders the overlay. (cdpFocusCacheAt is a Vue-reactive field
    // because Vue's reactivity intercepts the assignment in onmessage.)
    get debugRectStyles(): Array<Record<string, string>> {
      if (!this.debugRects) return []
      // Track cdpFocusCacheAt so the computed re-runs on each push.
      void this.cdpFocusCacheAt
      const rects = this.inputRectsCache
      if (!rects.length) return []
      const overlay = this._overlay?.getBoundingClientRect()
      if (!overlay) return []
      const vw = this.cdpFocusCache?.viewportWidth
      const vh = this.cdpFocusCache?.viewportHeight
      // CSS-crop mode: 1:1 mapping; otherwise scale overlay → server vp.
      let sx: number, sy: number
      if (this.mobileViewportActive) {
        sx = 1
        sy = 1
      } else {
        const rw = vw && vw > 0 ? vw : (this.width || overlay.width)
        const rh = vh && vh > 0 ? vh : (this.height || overlay.height)
        sx = overlay.width / rw
        sy = overlay.height / rh
      }
      return rects.map(r => ({
        left: `${overlay.left + r.x * sx}px`,
        top: `${overlay.top + r.y * sy}px`,
        width: `${r.width * sx}px`,
        height: `${r.height * sy}px`,
      }))
    }

    get isAndroid(): boolean {
      return /android/i.test(navigator.userAgent)
    }
    get isIOS(): boolean {
      const ua = navigator.userAgent
      if (/iPad|iPhone|iPod/.test(ua)) return true
      // iPadOS 13+ reports as Mac with touch support.
      if ((navigator.platform || '') === 'MacIntel' && (navigator.maxTouchPoints || 0) > 1) return true
      return false
    }

    // Inline style for the hidden proxy input. Keep it on-screen at the
    // top-left even when inactive so iOS doesn't fire a scroll-to-focused-
    // input dance when the input gains focus (the dance would visibly shift
    // the popcorn page upward, since iOS scrolls the visual viewport even
    // when document scrolling is disabled). Top-anchored = already above the
    // soft keyboard, so no scroll needed.
    get proxyInputStyle(): Record<string, string> {
      return {
        position: 'fixed',
        top: '0',
        left: '0',
        zIndex: '9999',
      }
    }

    // Touch state machine: distinguishes tap / swipe / long-press from a single touch stream.
    private touchMode: 'idle' | 'pending' | 'scroll' | 'longpress' = 'idle'
    private touchStartX = 0
    private touchStartY = 0
    private touchStartTime = 0
    private touchLastX = 0
    private touchLastY = 0
    private touchLastWheelEmit = 0
    // Fractional scroll carry: finger pixels not yet converted to a whole wheel
    // unit. Keeps slow/precise drags from being rounded away to zero.
    private scrollAccumX = 0
    private scrollAccumY = 0
    private longPressTimer: number | null = null

    // iOS-only: fired on touchstart so the focus-state RTT runs in parallel
    // with the tap itself. By the time touchend fires (~150–300 ms later)
    // the answer is usually already in hand and we can decide whether to
    // pop the keyboard without doing a blocking sync XHR — which on a slow
    // network would freeze the page for the duration of one round-trip.
    // Set to undefined when not yet resolved; null on transport error.
    private touchstartFocusResult: ActiveElementInfo | null | undefined = undefined

    // Mobile typing transport state. Each keystroke POSTs to /cdp/input on
    // the gateway-reachable API, which forwards into a server-side persistent
    // CDP session. A direct browser→:9222 WebSocket would be lower latency
    // but is unreachable in most deployments and silently drops keystrokes
    // on disconnect; HTTP requests are independent so a flaky cellular link
    // loses at most the in-flight char.

    get admin() {
      return this.$accessor.user.admin
    }

    get connected() {
      return this.$accessor.connected
    }

    get connecting() {
      return this.$accessor.connecting
    }

    get hosting() {
      return this.$accessor.remote.hosting
    }

    get implicitHosting() {
      return this.$accessor.remote.implicitHosting
    }

    get hosted() {
      return this.$accessor.remote.hosted
    }

    get volume() {
      return this.$accessor.video.volume
    }

    get muted() {
      return this.$accessor.video.muted
    }

    get stream() {
      return this.$accessor.video.stream
    }

    get playing() {
      return this.$accessor.video.playing
    }

    get playable() {
      return this.$accessor.video.playable
    }

    get emotes() {
      return this.$accessor.chat.emotes
    }

    get autoplay() {
      return this.$accessor.settings.autoplay
    }

    // server-side lock
    get controlLocked() {
      return 'control' in this.$accessor.locked && this.$accessor.locked['control'] && !this.$accessor.user.admin
    }

    get locked() {
      return this.$accessor.remote.locked || (this.controlLocked && (!this.hosting || this.implicitHosting))
    }

    get scroll() {
      return this.$accessor.settings.scroll
    }

    get scroll_invert() {
      return this.$accessor.settings.scroll_invert
    }

    get pip_available() {
      //@ts-ignore
      return typeof document.createElement('video').requestPictureInPicture === 'function'
    }

    get clipboard_read_available() {
      return (
        'clipboard' in navigator &&
        typeof navigator.clipboard.readText === 'function' &&
        // Firefox 122+ incorrectly reports that it can read the clipboard but it can't
        // instead it hangs when reading clipboard, until user clicks on the page
        // and the click itself is not handled by the page at all, also the clipboard
        // reads always fail with "Clipboard read operation is not allowed."
        navigator.userAgent.indexOf('Firefox') == -1
      )
    }

    get clipboard_write_available() {
      return 'clipboard' in navigator && typeof navigator.clipboard.writeText === 'function'
    }

    get clipboard() {
      return this.$accessor.remote.clipboard
    }

    get width() {
      return this.$accessor.video.width
    }

    get height() {
      return this.$accessor.video.height
    }

    get rate() {
      return this.$accessor.video.rate
    }

    get vertical() {
      return this.$accessor.video.vertical
    }

    get horizontal() {
      return this.$accessor.video.horizontal
    }

    // Whether the mobile-viewport CSS crop is active. The CSS shows only
    // the top-left screenW × screenH of the 1920×1080 framebuffer, so when
    // chromium is rendering a mobile layout (cdp-magnify.sh active) the
    // empty padding around it doesn't fill the user's screen.
    //
    // Two activation signals:
    //   1. The local magnify toggle (user tapped the in-app magnify button).
    //   2. The server's pushed viewportWidth shrunk below the native stream
    //      resolution — i.e. someone ran cdp-magnify.sh externally and the
    //      remote chromium is now mobile-sized. Auto-detect via the focus
    //      push so the script works standalone without requiring the
    //      viewer to also tap the in-app button.
    get mobileViewportActive(): boolean {
      if (!this.is_touch_device) return false
      const vw = this.cdpFocusCache?.viewportWidth || 0
      const remoteShrunk = vw > 0 && this.width > 0 && vw < this.width - 50
      return !this.isMagnified || remoteShrunk
    }

    toggleMagnify() {
      this.isMagnified = !this.isMagnified
      this.applyViewportEmulation()
    }

    // Close the open popup window. Fullscreen popups (window.open) have no
    // browser chrome, so this in-app button is the only way to dismiss one.
    // The server closes the tracked popup target and pushes popup:false back
    // over the input-ws; we clear optimistically so the button hides at once.
    closePopup() {
      const url = this.resolveCDPUrl('close-popup')
      fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      })
        .then(() => { this.popupOpen = false })
        .catch((err) => console.warn('[CDP] close-popup failed', err))
    }

    // Answer a JS dialog (alert/confirm/prompt). Posts the choice to the server,
    // which calls Page.handleJavaScriptDialog on the dialog's session. We clear
    // optimistically; the server also pushes dialog:null once it's dismissed.
    respondDialog(accept: boolean) {
      const d = this.pendingDialog
      if (!d) return
      const promptText = accept ? this.dialogPromptText : ''
      this.pendingDialog = null
      const url = this.resolveCDPUrl('dialog-respond')
      fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: d.id, accept, promptText }),
      }).catch((err) => console.warn('[CDP] dialog-respond failed', err))
    }

    // Server endpoint that runs Emulation.* against the upstream chromium.
    // Mirrors the URL pattern of /cdp/active-element (gateway-routed in prod).
    private getEmulateDeviceUrl(): string {
      return this.resolveCDPUrl('emulate-device')
    }

    // Sync the remote chromium's emulated viewport. Mobile (mobileViewport-
    // Active): lay out at the viewer's window size into the top-left of the
    // frame so the visible region matches the CSS crop. Desktop: emulate the
    // full 1920×1080 stream (shown letterboxed).
    private applyViewportEmulation(opts?: { visibleHeight?: number }) {
      const url = this.getEmulateDeviceUrl()
      const physW = this.width || 1920
      const physH = this.height || 1080

      let body: Record<string, unknown>
      if (this.mobileViewportActive) {
        // When the soft keyboard is up, opts.visibleHeight is the area above
        // the keyboard — emulate the remote at that height so it reflows,
        // scrolls the focused field into view, and stays scrollable.
        const mobileH = opts?.visibleHeight ?? window.innerHeight
        body = {
          width: Math.max(1, Math.round(window.innerWidth)),
          height: Math.max(1, Math.round(Math.min(mobileH, physH))),
          mobile: true,
          // deviceScaleFactor: 1 — DPR>1 makes chromium render at 2× and
          // overflow the 1920×1080 stream, then the CSS crop misses the
          // bottom rows.
          deviceScaleFactor: 1,
          touch: true,
          maxTouchPoints: 5,
          // NOTE: we deliberately do NOT push the viewer's phone UA here.
          // Emulation.setUserAgentOverride only changes the UA *string* — not
          // userAgentMetadata (Client Hints) or navigator.platform — so a phone
          // UA on top of CloakBrowser's Windows fingerprint + Linux Chromium
          // (Blink, not WebKit) is an incoherent identity that reCAPTCHA
          // Enterprise / Akamai flag ("score too low"). CloakBrowser already
          // presents ONE coherent identity (UA + CH + platform + WebGL + TLS);
          // we emulate only the viewport + touch and leave that identity intact.
        }
      } else {
        // Desktop: emulate at the full stream size (the remote stays 1920×1080
        // and is shown letterboxed — no client-side scaling/crop).
        body = { width: physW, height: physH, mobile: false, deviceScaleFactor: 1, touch: false }
      }

      // Skip redundant POSTs: onResize can fire many times at the same size.
      const key = JSON.stringify(body)
      if (key === this.lastEmulatedKey) return
      this.lastEmulatedKey = key

      fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }).catch((err) => console.warn('[CDP] emulate-device failed', err))
    }

    // Auto-select the browsing experience for the connecting viewer's device:
    // touch devices get a native mobile-like layout (mobile viewport + touch +
    // the viewer's real UA). Desktop keeps the native letterboxed view. Runs on
    // connect and after viewport/orientation changes. iframe-safe:
    // is_touch_device and navigator.userAgent both propagate into embedded
    // frames, and the emulated size is the iframe's own box.
    private autoApplyDeviceExperience() {
      if (!this.hosting) return
      if (this.is_touch_device) {
        // Mobile-like: activate the CSS crop, which flips mobileViewportActive
        // true so applyViewportEmulation pushes the mobile override.
        this.isMagnified = false
      }
      if (this.mobileViewportActive) {
        this.applyViewportEmulation()
      }
    }

    // Debounced re-apply of mobile emulation on viewport/orientation change
    // (phone rotation, embed resize).
    private scheduleDeviceExperience() {
      if (!this.is_touch_device) return
      if (this.deviceExperienceTimer !== null) {
        window.clearTimeout(this.deviceExperienceTimer)
      }
      this.deviceExperienceTimer = window.setTimeout(() => {
        this.deviceExperienceTimer = null
        if (this.hosting && this.mobileViewportActive) {
          this.applyViewportEmulation()
        }
      }, 250)
    }

    @Watch('hosting')
    onHostingChangedForDevice(now: boolean) {
      if (now) this.autoApplyDeviceExperience()
    }

    // Shrink the remote viewport to the area above the soft keyboard so the
    // remote page reflows, scrolls the focused field into view, and the content
    // behind the keyboard becomes reachable by scrolling. The mobile crop shows
    // the stream 1:1 from the top-left, so the reduced render lands exactly in
    // the visible area. No-op unless the mobile layout is active.
    private applyKeyboardViewport(visibleHeight: number) {
      if (!this.mobileViewportActive) return
      const vh = Math.round(visibleHeight)
      // Skip churn while the keyboard animates — only re-emulate on a real
      // height change.
      if (Math.abs(vh - this.lastEmulatedVisibleHeight) <= 20) return
      this.lastEmulatedVisibleHeight = vh
      this.applyViewportEmulation({ visibleHeight: vh })
    }

    // Restore the full remote viewport after the keyboard is dismissed.
    private restoreViewportAfterKeyboard() {
      if (this.lastEmulatedVisibleHeight === 0) return
      this.lastEmulatedVisibleHeight = 0
      if (this.mobileViewportActive) this.applyViewportEmulation()
    }

    get is_touch_device() {
      return (
        // detect if the device has touch support
        ('ontouchstart' in window || navigator.maxTouchPoints > 0) &&
        // the primary input mechanism includes a pointing device of
        // limited accuracy, such as a finger on a touchscreen.
        window.matchMedia('(pointer: coarse)').matches
      )
    }

    @Watch('iframeOffset')
    onIframeOffsetChanged(offset: number) {
      if (!this._player) return
      // Lift the streamed video element so the focused remote field clears
      // the on-screen keyboard. Offset is negative when lifting.
      this._player.style.transform = offset !== 0 ? `translateY(${offset}px)` : ''
      this._player.style.transition = 'transform 0.3s ease-out'
    }

    @Watch('width')
    onWidthChanged() {
      this.onResize()
    }

    @Watch('height')
    onHeightChanged() {
      this.onResize()
    }

    @Watch('volume')
    onVolumeChanged(volume: number) {
      volume /= 100

      if (this._video && this._video.volume != volume) {
        this._video.volume = volume
      }
    }

    @Watch('muted')
    onMutedChanged(muted: boolean) {
      if (this._video && this._video.muted != muted) {
        this._video.muted = muted

        if (!muted) {
          this.mutedOverlay = false
        }
      }
    }

    @Watch('stream')
    onStreamChanged(stream?: MediaStream) {
      if (!this._video || !stream) {
        return
      }

      if ('srcObject' in this._video) {
        this._video.srcObject = stream
      } else {
        // @ts-ignore
        this._video.src = window.URL.createObjectURL(this.stream) // for older browsers
      }
    }

    @Watch('playing')
    async onPlayingChanged(playing: boolean) {
      // In Safari, native events can fire slightly before the `video.paused` property flips.
      // This anti-echo guard prevents the watcher from fighting the video element's own state changes.
      if (this.isVideoSyncing) return;

      if (this._video && this._video.paused && playing) {
        // if autoplay is disabled, play() will throw an error
        // and we need to properly save the state otherwise we
        // would be thinking we're playing when we're not
        try {
          await this._video.play()
        } catch (err: any) {
          if (!this._video.muted) {
            // video.play() can fail if audio is set due restrictive
            // browsers autoplay policy -> retry with muted audio
            try {
              this.$accessor.video.setMuted(true)
              this._video.muted = true
              await this._video.play()
            } catch (err: any) {
              // if it still fails, we're not playing anything
              this.$accessor.video.pause()
            }
          } else {
            this.$accessor.video.pause()
          }
        }
      }

      if (this._video && !this._video.paused && !playing) {
        this.pause()
      }
    }

    @Watch('clipboard')
    async onClipboardChanged(clipboard: string) {
      if (this.clipboard_write_available) {
        try {
          await navigator.clipboard.writeText(clipboard)
          this.$accessor.remote.setClipboard(clipboard)
        } catch (err: any) {
          this.$log.error(err)
        }
      }
    }

    mounted() {
      this._container.addEventListener('resize', this.onResize)
      this.onVolumeChanged(this.volume)
      this.onMutedChanged(this.muted)
      this.onStreamChanged(this.stream)
      this.onResize()

      this.observer.observe(this._component)

      // Open the persistent input WebSocket. Falls back transparently to HTTP
      // POSTs in postCDPInput when the WS isn't connected yet or has died.
      this.connectInputWS()

      // If already hosting at mount (e.g. reconnect), pick the device
      // experience now; otherwise the hosting watcher handles the transition.
      if (this.hosting) {
        this.autoApplyDeviceExperience()
      }

      // Mobile keyboard detection via visualViewport (ported from
      // keyboard.ts:214-253). When the visual viewport shrinks by >50px the
      // soft keyboard is up; when it grows back the user dismissed it.
      if (this.is_touch_device && window.visualViewport) {
        window.visualViewport.addEventListener('resize', this.handleViewportResize)
      }

      // Inbound postMessage listener — portal → popcorn replies for the
      // select-dropdown bridge. Listening unconditionally so portals can
      // ping us even before we send POPCORN_SHOW_SELECT in a future iter.
      window.addEventListener('message', this.onPortalMessage)

      // Tell any embedding portal that our touch/keyboard layer is ready.
      // Per the popcorn ↔ portal protocol; harmless when not iframe-embedded.
      try {
        window.parent.postMessage({ type: 'POPCORN_INPUT_READY' }, '*')
      } catch { /* no parent or cross-origin */ }

      onFullscreenChange(this._player, () => {
        this.fullscreen = isFullscreen()
        this.fullscreen ? lockKeyboard() : unlockKeyboard()
        this.onResize()
      })

      this._video.addEventListener('canplaythrough', () => {
        console.log('[DEBUG] canplaythrough fired');
        this.$accessor.video.setPlayable(true)
        if (this.autoplay) {
          this.$nextTick(() => {
            console.log('[DEBUG] canplaythrough calling $accessor.video.play()');
            this.$accessor.video.play()
          })
        }
      })

      this._video.addEventListener('ended', () => {
        console.log('[DEBUG] ended fired');
        this.$accessor.video.setPlayable(false)
      })

      this._video.addEventListener('error', (event) => {
        console.log('[DEBUG] error fired', event.error);
        this.$log.error(event.error)
        this.$accessor.video.setPlayable(false)
      })

      this._video.addEventListener('volumechange', () => {
        this.$accessor.video.setMuted(this._video.muted)
        this.$accessor.video.setVolume(this._video.volume * 100)
      })

      this._video.addEventListener('playing', () => {
        this.isVideoSyncing = true
        this.$accessor.video.play()
        this.$nextTick(() => { this.isVideoSyncing = false })
      })

      this._video.addEventListener('pause', () => {
        this.isVideoSyncing = true
        this.$accessor.video.pause()
        this.$nextTick(() => { this.isVideoSyncing = false })
      })

      /* Initialize Guacamole Keyboard */
      this.keyboard.onkeydown = (key: number) => {
        if (!this.hosting || this.locked) {
          return true
        }

        this.$client.sendData('keydown', { key: this.keyMap(key) })

        // Allow Ctrl/Cmd+V through so the browser fires a paste event,
        // which triggers onPaste -> syncClipboard (required for Safari
        // clipboard access since it only permits reads in user-initiated events)
        const { ctrl, meta } = this.keyboard.modifiers
        return key === 0x0076 && !!(ctrl || meta)
      }
      this.keyboard.onkeyup = (key: number) => {
        if (!this.hosting || this.locked) {
          return
        }

        this.$client.sendData('keyup', { key: this.keyMap(key) })
      }
      // On touch devices, iOS autocomplete/Gboard swipe-typing/CJK IMEs only
      // fire `input` events with no usable keydown — guacamole-keyboard would
      // miss them. We handle those via @beforeinput → onBeforeInput, so don't
      // double-attach here.
      if (!this.is_touch_device) {
        this.keyboard.listenTo(this._overlay)
      }
    }

    beforeDestroy() {
      this.cancelLongPress()
      if (this.pendingBackspaceTimer !== null) {
        window.clearTimeout(this.pendingBackspaceTimer)
        this.pendingBackspaceTimer = null
      }
      if (window.visualViewport) {
        window.visualViewport.removeEventListener('resize', this.handleViewportResize)
      }
      window.removeEventListener('message', this.onPortalMessage)
      if (this.deviceExperienceTimer !== null) {
        window.clearTimeout(this.deviceExperienceTimer)
        this.deviceExperienceTimer = null
      }
      this.disconnectInputWS()
      this.stopStuckStatePoller()
      this.observer.disconnect()
      this.$accessor.video.setPlayable(false)
      /* Guacamole Keyboard does not provide destroy functions */
    }

    get hasMacOSKbd() {
      return /(Mac|iPhone|iPod|iPad)/i.test(navigator.platform)
    }

    KeyTable = {
      XK_ISO_Level3_Shift: 0xfe03, // AltGr
      XK_Mode_switch: 0xff7e, // Character set switch
      XK_Control_L: 0xffe3, // Left control
      XK_Control_R: 0xffe4, // Right control
      XK_Meta_L: 0xffe7, // Left meta
      XK_Meta_R: 0xffe8, // Right meta
      XK_Alt_L: 0xffe9, // Left alt
      XK_Alt_R: 0xffea, // Right alt
      XK_Super_L: 0xffeb, // Left super
      XK_Super_R: 0xffec, // Right super
    }

    keyMap(key: number): number {
      // Alt behaves more like AltGraph on macOS, so shuffle the
      // keys around a bit to make things more sane for the remote
      // server. This method is used by noVNC, RealVNC and TigerVNC
      // (and possibly others).
      if (this.hasMacOSKbd) {
        switch (key) {
          case this.KeyTable.XK_Meta_L:
            key = this.KeyTable.XK_Control_L
            break
          case this.KeyTable.XK_Super_L:
            key = this.KeyTable.XK_Alt_L
            break
          case this.KeyTable.XK_Super_R:
            key = this.KeyTable.XK_Super_L
            break
          case this.KeyTable.XK_Alt_L:
            key = this.KeyTable.XK_Mode_switch
            break
          case this.KeyTable.XK_Alt_R:
            key = this.KeyTable.XK_ISO_Level3_Shift
            break
        }
      }

      return key
    }

    // --- CDP Logic ---
    // Resolve the URL for a /cdp/<endpoint> call across all three deployment
    // shapes:
    //   1. Production (path /<browser_pod_id>/<session_id>/<token>/...)
    //      → routed through the gateway at /api/<sid>/<tok>/cdp/<endpoint>.
    //   2. HTTPS without that path prefix (cloudflared quick-tunnel, any
    //      reverse-proxy that exposes both neko and kernel-images-api on the
    //      same origin) → use same-origin /cdp/<endpoint>. The proxy is
    //      responsible for path-routing /cdp/* to the kernel-images-api.
    //      Hitting `http://host:9222` from an HTTPS page would be blocked as
    //      mixed content even if the port were reachable, which it isn't
    //      through cloudflared.
    //   3. HTTP local dev → hit the kernel-images-api directly on :9222.
    private resolveCDPUrl(endpoint: string): string {
      const match = window.location.pathname.match(/^\/(browser[^/]+)\/([^/]+)\/([^/]+)/)
      if (match) {
        return `${window.location.origin}/api/${match[2]}/${match[3]}/cdp/${endpoint}`
      }
      if (window.location.protocol === 'https:') {
        return `${window.location.origin}/cdp/${endpoint}`
      }
      return `http://${window.location.hostname}:9222/cdp/${endpoint}`
    }

    getActiveElementUrl(): string {
      return this.resolveCDPUrl('active-element')
    }

    private getCDPInputUrl(): string {
      return this.resolveCDPUrl('input')
    }

    // Outbox for /cdp/input. Single in-flight POST at a time + retry-with-
    // backoff gives us ordering and resilience on lossy networks without
    // needing server-side dedup. The previous fire-and-forget approach lost
    // keystrokes silently on transient failures and could reorder bursts
    // when two POSTs were in flight and the network reordered packets.
    private cdpInputOutbox: Array<{
      seq: number
      body: { text?: string; key?: string; count?: number }
      attempts: number
    }> = []
    private cdpInputInFlight = false
    private cdpInputSeq = 0
    // Outbox queue: bound size, max retries per entry, per-request timeout.
    // Overflow drops the oldest because the user is actively typing newer
    // chars; nobody cares about a 5-second-stale keystroke.
    private readonly CDP_OUTBOX_MAX = 128
    private readonly CDP_MAX_ATTEMPTS = 5
    // Tuned for cellular networks where a single POST can easily take >10 s
    // through Cloudflare under packet loss. 5 s was too aggressive — we were
    // aborting requests that would have succeeded a few hundred ms later,
    // then paying backoff on top.
    private readonly CDP_REQUEST_TIMEOUT_MS = 20000
    // Soft cap per coalesced POST so a stuck connection doesn't accumulate a
    // 10 KB payload that takes even longer to push through. 256 chars is well
    // under any URL/keepalive limit and still big enough that a fast typist
    // collapses ~5 s of input into a single POST.
    private readonly CDP_TEXT_BATCH_MAX = 256

    // Persistent WebSocket to /cdp/input-ws. When connected we send keystroke
    // frames over it instead of POSTing to /cdp/input — on high-RTT cellular
    // links this skips per-request TCP/TLS setup and cuts the per-keystroke
    // round-trips roughly in half. On WS close/failure we transparently fall
    // back to the HTTP outbox.
    private cdpInputWS: WebSocket | null = null
    private cdpInputWSReady = false
    private cdpInputWSReconnect: number | null = null
    private cdpInputWSReconnectAttempts = 0
    // Outstanding hit-test requests, keyed by seq. Resolves when the
    // matching {type:"hitTest", seq} reply arrives on the WS.
    private cdpHitTestPending: Map<number, (r: HitTestReply | null) => void> = new Map()

    // Tap-time bookkeeping for the focus-driven keyboard:
    //  - pendingTapAt: timestamp of the most recent tap (touchend → click).
    //  - pendingTapPreFocusKey: focusKey snapshot at tap time.
    //  - postTapTimer: scheduled check that runs after enough wall-time has
    //    passed for a focus event to reach us from the remote. If focus
    //    DIDN'T change but it was previously on an input, the tap must have
    //    landed on a non-focusable area — dismiss the keyboard.
    private pendingTapAt = 0
    private pendingTapPreFocusKey: string | null = null
    private postTapTimer: number | null = null

    // Tier-1 input-rects cache. Populated from each WS focus push (which on
    // the server runs every 200 ms and pushes whenever rects change or on a
    // 600 ms heartbeat). Tap-time hit-test is O(N) local — *no* network
    // round-trip — so the decision works identically at 50 ms or 5 s RTT.
    // Cache freshness ≈ RTT; on a fast network rects are ~250 ms stale, on
    // a 3 s RTT cellular link they're up to ~3 s stale. Tap handler treats
    // "cache not yet populated" as "unknown" and falls back to leaving the
    // keyboard state alone (user can use the toolbar keyboard icon).
    private inputRectsCache: InputRect[] = []
    private inputRectsReady = false
    // Last time the user scrolled. The remote moves rects under our cached
    // positions when it processes the scroll, but on a 3 s RTT cellular link
    // we don't get the corrected snapshot back for ~one round-trip. During
    // that window the cache is wrong; treat it as 'unknown' to avoid
    // false dismisses while the user is mid-flick.
    private lastScrollAt = 0
    private readonly SCROLL_CACHE_INVALID_MS = 1500

    // Stuck-state poller bookkeeping: when the WS-pushed focus snapshot
    // shows the remote activeElement is NOT a text input for N consecutive
    // ticks while our keyboard is up, the remote page must have moved
    // focus off the input (route change, modal close, blur'd by JS). Force
    // the local keyboard down so the user isn't stuck typing into a void.
    private stuckTimer: number | null = null
    private stuckMissCount = 0

    private getCDPInputWSUrl(): string {
      // Mirror the HTTPS-aware logic of resolveCDPUrl, but swap http→ws.
      const match = window.location.pathname.match(/^\/(browser[^/]+)\/([^/]+)\/([^/]+)/)
      const wsScheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const wsOrigin = `${wsScheme}//${window.location.host}`
      if (match) {
        return `${wsOrigin}/api/${match[2]}/${match[3]}/cdp/input-ws`
      }
      if (window.location.protocol === 'https:') {
        return `${wsOrigin}/cdp/input-ws`
      }
      return `${wsScheme}//${window.location.hostname}:9222/cdp/input-ws`
    }

    private connectInputWS() {
      if (this.cdpInputWS) return
      const url = this.getCDPInputWSUrl()
      let ws: WebSocket
      try {
        ws = new WebSocket(url)
      } catch (err) {
        console.warn('[CDP-WS] construct failed, will retry:', err)
        this.scheduleInputWSReconnect()
        return
      }
      this.cdpInputWS = ws

      ws.onopen = () => {
        this.cdpInputWSReady = true
        this.cdpInputWSReconnectAttempts = 0
        console.log('[CDP-WS] connected')
      }
      ws.onclose = () => {
        const wasReady = this.cdpInputWSReady
        this.cdpInputWS = null
        this.cdpInputWSReady = false
        if (wasReady) console.warn('[CDP-WS] closed; falling back to HTTP outbox')
        this.scheduleInputWSReconnect()
      }
      ws.onerror = (e) => {
        console.warn('[CDP-WS] error', e)
        // onclose follows automatically.
      }
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data)
          if (msg?.type === 'hitTest' && typeof msg.seq === 'number') {
            const cb = this.cdpHitTestPending.get(msg.seq)
            if (cb) {
              this.cdpHitTestPending.delete(msg.seq)
              if (msg.err) {
                cb(null)
              } else {
                cb({
                  isInput: !!msg.isInput,
                  tag: msg.tag,
                  readonly: !!msg.readonly,
                  disabled: !!msg.disabled,
                  isEditable: !!msg.isEditable,
                  focusKey: msg.focusKey,
                })
              }
            }
            return
          }
          if (msg?.type === 'focus' && msg.info) {
            const prev = this.cdpFocusCache
            const rects = Array.isArray(msg.info.inputRects) ? msg.info.inputRects : []
            this.cdpFocusCache = {
              isInput: !!msg.info.isInput,
              tag: msg.info.tag || 'unknown',
              type: msg.info.type,
              isEditable: msg.info.isEditable,
              readonly: !!msg.info.readonly,
              disabled: !!msg.info.disabled,
              focusKey: msg.info.focusKey,
              elementTop: msg.info.elementTop,
              elementHeight: msg.info.elementHeight,
              elementLeft: msg.info.elementLeft,
              elementWidth: msg.info.elementWidth,
              selectInfo: msg.info.selectInfo,
              inputRects: rects,
              viewportWidth: msg.info.viewportWidth,
              viewportHeight: msg.info.viewportHeight,
              inputType: msg.info.inputType,
              inputMode: msg.info.inputMode,
              autoComplete: msg.info.autoComplete,
              enterKeyHint: msg.info.enterKeyHint,
            }
            this.cdpFocusCacheAt = Date.now()
            this.inputRectsCache = rects
            this.inputRectsReady = true
            this.onFocusSnapshotUpdated(prev)
            return
          }
          // Popup open/closed: a fullscreen popup window (window.open) has no
          // browser chrome to close it, so we surface an in-app close button.
          if (msg?.type === 'popup') {
            this.popupOpen = !!msg.open
            return
          }
          // JS dialog opened/cleared on the remote — render/hide our overlay.
          if (msg?.type === 'dialog') {
            this.pendingDialog = msg.dialog || null
            this.dialogPromptText = (msg.dialog && msg.dialog.defaultPrompt) || ''
            return
          }
          // Ack — informational. Order is preserved by TCP.
          if (msg?.ok === false) {
            console.warn('[CDP-WS] server rejected frame:', msg.err)
          }
        } catch { /* malformed frame, ignore */ }
      }
    }

    private scheduleInputWSReconnect() {
      if (this.cdpInputWSReconnect !== null) return
      const attempt = ++this.cdpInputWSReconnectAttempts
      // Backoff: 500ms, 1s, 2s, 4s, capped at 8s.
      const delay = Math.min(500 * (1 << Math.min(attempt - 1, 4)), 8000)
      this.cdpInputWSReconnect = window.setTimeout(() => {
        this.cdpInputWSReconnect = null
        this.connectInputWS()
      }, delay)
    }

    // Send a hit-test query over the WS asking "what element is under
    // (remoteX, remoteY)?". Returns null on timeout / WS not ready.
    // This bypasses the activeElement-staleness problem entirely: clicks
    // on non-focusable areas don't blur the previous input, so isInput
    // stays true; elementFromPoint asks the only question that matters.
    private hitTestAtRemote(remoteX: number, remoteY: number): Promise<HitTestReply | null> {
      return new Promise((resolve) => {
        if (!this.cdpInputWSReady || !this.cdpInputWS || this.cdpInputWS.readyState !== WebSocket.OPEN) {
          resolve(null)
          return
        }
        this.cdpInputSeq++
        const seq = this.cdpInputSeq
        const timeout = window.setTimeout(() => {
          this.cdpHitTestPending.delete(seq)
          resolve(null)
        }, 6000)
        this.cdpHitTestPending.set(seq, (r) => {
          window.clearTimeout(timeout)
          resolve(r)
        })
        try {
          this.cdpInputWS.send(JSON.stringify({ seq, hitTest: { x: remoteX, y: remoteY } }))
        } catch {
          window.clearTimeout(timeout)
          this.cdpHitTestPending.delete(seq)
          resolve(null)
        }
      })
    }

    // Called whenever a fresh focus snapshot arrives over the WS (or via
    // the HTTP fetch fallback). This is the BACKUP path — the tap handler
    // already made an instant decision via the input-rects cache. This
    // catches:
    //   - Remote programmatic focus changes (form submit advances cursor,
    //     route change focuses a new field, etc.) where no tap happened.
    //   - Stuck-state recovery: focus moved off any input → dismiss.
    private onFocusSnapshotUpdated(prev: ActiveElementInfo | null) {
      if (!this.is_touch_device) return
      const cur = this.cdpFocusCache
      const prevIsInput = !!(prev && prev.isInput && !prev.readonly && !prev.disabled)
      const curIsInput = !!(cur && cur.isInput && !cur.readonly && !cur.disabled)
      const prevKey = prev?.focusKey || null
      const curKey = cur?.focusKey || null

      // Remote focus moved to a new text input *without* a recent tap →
      // programmatic focus on the remote. Pop the keyboard.
      const recentTap = Date.now() - this.pendingTapAt < 4000
      if (curIsInput && curKey !== prevKey && !this.keyboardActive &&
          !this.keyboardJustDismissed && !recentTap) {
        this.focusProxyInputForIOS()
        this.stuckMissCount = 0
        return
      }

      // Focus moved between inputs (e.g. form-tab) while kbd is up:
      // keep kbd open but re-apply the IME shaping for the new field.
      // Without this, tabbing from a text field to a number field
      // leaves Gboard on QWERTY when the user expects a numeric pad.
      if (curIsInput && this.keyboardActive && curKey !== prevKey) {
        this.applyProxyImeHints()
      }

      // Focus moved off any input while kbd is up → dismiss.
      if (prevIsInput && !curIsInput && this.keyboardActive) {
        this.dismissKeyboard('remote-focus-lost')
        this.stuckMissCount = 0
      }
    }

    // Stuck-state poller: when kbd is up, watch the WS-pushed focus
    // snapshot for "no input focused on remote" — if that holds for 2
    // consecutive checks, the remote moved focus and we missed the
    // transition (or the WS reconnected mid-flight). Force the kbd down.
    private startStuckStatePoller() {
      if (this.stuckTimer !== null) return
      this.stuckMissCount = 0
      this.stuckTimer = window.setInterval(() => {
        if (!this.keyboardActive) return
        if (this.keyboardOpening) return
        if (this.keyboardJustDismissed) return
        const cur = this.cdpFocusCache
        if (!cur) return
        const curIsInput = cur.isInput && !cur.readonly && !cur.disabled
        if (curIsInput) {
          this.stuckMissCount = 0
          return
        }
        this.stuckMissCount++
        if (this.stuckMissCount >= 2) {
          this.stuckMissCount = 0
          this.dismissKeyboard('stuck-state')
        }
      }, 1000)
    }

    private stopStuckStatePoller() {
      if (this.stuckTimer !== null) {
        window.clearInterval(this.stuckTimer)
        this.stuckTimer = null
      }
      this.stuckMissCount = 0
    }

    // Bulletproof keyboard dismiss. iOS Safari sometimes keeps the system
    // keyboard visible after a bare `.blur()` — the cure is to unwind every
    // piece of state at once (mirrors reclaim-portal's
    // dismissKeyboardFromViewport):
    //   - flip our local keyboardActive immediately so subsequent checks
    //     see "down"
    //   - stop the stuck-state poller (no point polling once we're down)
    //   - clear allowBlur+keyboardJustDismissed guards so the next tap
    //     can decide cleanly
    //   - blur the proxy AND clear its value (Samsung Keyboard treats
    //     residual proxy.value as "current word" and commits it on next
    //     keystroke, corrupting the next field)
    //   - window.scrollTo(0, 0) to undo any iOS scroll-to-focused-input
    //     dance
    //   - briefly remove `readonly` if it was set, then re-blur — some
    //     iOS builds need the round trip to actually hide the IME
    private dismissKeyboard(reason: string) {
      if (this.debugRects) console.log('[kbd] dismiss', reason)
      this.allowBlur = true
      this.keyboardActive = false
      this.stopStuckStatePoller()
      this.iframeOffset = 0
      window.scrollTo(0, 0)
      this.restoreViewportAfterKeyboard()
      this.keyboardJustDismissed = true
      window.setTimeout(() => { this.keyboardJustDismissed = false }, 150)
      const proxy = this._proxyInput
      if (proxy) {
        proxy.value = ''
        proxy.blur()
        // iOS-only hammer: setAttribute('readonly') + re-blur shakes the
        // system out of "keyboard visible but no focus" states that arise
        // when the user dismisses via swipe-down then taps elsewhere.
        if (this.isIOS) {
          try { proxy.setAttribute('readonly', 'readonly') } catch { /* SSR */ }
          window.setTimeout(() => {
            try { proxy.removeAttribute('readonly') } catch { /* SSR */ }
          }, 0)
        }
        // Android hammer: Gboard / Samsung Keyboard often keep the IME
        // visible after bare blur() — they only re-evaluate "should the
        // kbd hide?" on a *focus change*, not on blur alone. Force one
        // by briefly setting inputmode=none (HTML spec: explicitly
        // suppresses the virtual keyboard for this input) and focusing a
        // hidden off-screen input, then blurring + removing it. After
        // this round-trip the IME has seen "focused input that doesn't
        // want a kbd → hide", which sticks.
        if (this.isAndroid) {
          try { proxy.setAttribute('inputmode', 'none') } catch { /* noop */ }
          const tempInput = document.createElement('input')
          tempInput.type = 'text'
          tempInput.setAttribute('inputmode', 'none')
          tempInput.setAttribute('readonly', 'readonly')
          tempInput.style.cssText = 'position:fixed;top:-1000px;left:-1000px;width:1px;height:1px;opacity:0;font-size:16px;'
          document.body.appendChild(tempInput)
          tempInput.focus()
          window.setTimeout(() => {
            try { tempInput.blur() } catch { /* noop */ }
            if (tempInput.parentNode) tempInput.parentNode.removeChild(tempInput)
            try { proxy.removeAttribute('inputmode') } catch { /* noop */ }
          }, 50)
        }
      }
      try {
        window.parent.postMessage({
          type: 'POPCORN_VIEWPORT',
          visibleHeight: window.visualViewport ? window.visualViewport.height : window.innerHeight,
          occludedBottom: 0,
        }, '*')
      } catch { /* no parent */ }
      window.setTimeout(() => { this.allowBlur = false }, 150)
    }

    // Post-tap check. We don't know at tap time whether the user tapped an
    // input, a button, or empty space. The remote will tell us via a
    // focusin/focusout/no-event, but on cellular networks that takes
    // multiple seconds. Approach: poll on a short interval until we
    // observe a focus snapshot received AFTER the tap. Once we have a
    // post-tap snapshot, decide:
    //   - focusKey changed → handled by onFocusSnapshotUpdated (no-op here)
    //   - focusKey unchanged → user tapped a non-focusable area → dismiss
    //     the keyboard (browsers don't fire focusout for these cases).
    // Give up after MAX_WAIT_MS so we don't sit in this loop forever.
    private schedulePostTapDismissCheck() {
      if (this.postTapTimer !== null) {
        window.clearTimeout(this.postTapTimer)
      }
      const tapAt = this.pendingTapAt
      const preKey = this.pendingTapPreFocusKey
      const MAX_WAIT_MS = 8000
      const POLL_MS = 250
      const tick = () => {
        this.postTapTimer = null
        if (this.pendingTapAt !== tapAt) return // superseded by newer tap
        const elapsed = Date.now() - tapAt
        const gotPostTapUpdate = this.cdpFocusCacheAt > tapAt
        if (gotPostTapUpdate) {
          if (!this.keyboardActive) return
          const curKey = this.cdpFocusCache?.focusKey || null
          if (curKey === preKey) {
            // Focus didn't change → non-focusable tap → dismiss.
            this.allowBlur = true
            if (this._proxyInput) this._proxyInput.blur()
            window.setTimeout(() => { this.allowBlur = false }, 100)
          }
          return
        }
        if (elapsed >= MAX_WAIT_MS) return // give up
        this.postTapTimer = window.setTimeout(tick, POLL_MS)
      }
      this.postTapTimer = window.setTimeout(tick, POLL_MS)
    }

    private disconnectInputWS() {
      if (this.cdpInputWSReconnect !== null) {
        window.clearTimeout(this.cdpInputWSReconnect)
        this.cdpInputWSReconnect = null
      }
      if (this.cdpInputWS) {
        try { this.cdpInputWS.close() } catch { /* already closed */ }
        this.cdpInputWS = null
      }
      this.cdpInputWSReady = false
    }

    // Enqueue a single input payload. Coalesces consecutive text/key entries
    // into the queued tail so a fast typist on a slow link sends ONE large
    // POST instead of dozens of small ones — the bottleneck is RTT per
    // request, not chromium's ability to ingest characters. Ordering is
    // preserved: the outbox drains one POST at a time.
    // Pixel-precise scroll over the persistent input WS → Input.dispatchMouseEvent
    // {mouseWheel} on the active target. Smooth (sub-notch) and reaches popups,
    // unlike the neko XTest wheel. Returns true if sent; false when the WS isn't
    // up so the caller falls back to the neko wheel path. Best-effort: no ack and
    // no HTTP fallback — scroll is high-frequency and a dropped frame is fine.
    private sendCDPScroll(x: number, y: number, deltaX: number, deltaY: number): boolean {
      if (!this.cdpInputWSReady || !this.cdpInputWS || this.cdpInputWS.readyState !== WebSocket.OPEN) {
        return false
      }
      try {
        this.cdpInputWS.send(JSON.stringify({ scroll: { x, y, deltaX, deltaY } }))
        return true
      } catch {
        return false
      }
    }

    private postCDPInput(body: { text?: string; key?: string; count?: number }) {
      // Fast path: when the persistent WS is open, send a single frame and
      // return. TCP guarantees ordering on a single socket, so we don't need
      // the single-in-flight outbox here. Coalescing happens upstream at the
      // call site (IME composition end already batches into one Text call).
      if (this.cdpInputWSReady && this.cdpInputWS && this.cdpInputWS.readyState === WebSocket.OPEN) {
        try {
          this.cdpInputSeq++
          this.cdpInputWS.send(JSON.stringify({ seq: this.cdpInputSeq, ...body }))
          return
        } catch (err) {
          console.warn('[CDP-WS] send failed, falling back to HTTP:', err)
          // fall through to HTTP outbox path
        }
      }

      const tail = this.cdpInputOutbox[this.cdpInputOutbox.length - 1]
      // Only coalesce into entries that aren't already on the wire (the head
      // when in-flight). Modifying an in-flight body would change the bytes
      // chromium sees mid-request.
      const tailIsQueued = tail && !(this.cdpInputInFlight && tail === this.cdpInputOutbox[0])

      if (tailIsQueued && tail && body.text && tail.body.text != null && tail.body.key == null) {
        // Merge consecutive text fragments while we stay under the soft cap.
        const merged = tail.body.text + body.text
        if (merged.length <= this.CDP_TEXT_BATCH_MAX) {
          tail.body.text = merged
          return
        }
      } else if (
        tailIsQueued && tail && body.key && body.key === tail.body.key &&
        tail.body.text == null
      ) {
        // Same special key (Backspace/Enter/Tab) — bump count instead of
        // queuing a sibling entry. Cap matches the server's per-request limit.
        const inc = body.count || 1
        const nextCount = (tail.body.count || 1) + inc
        if (nextCount <= 64) {
          tail.body.count = nextCount
          return
        }
      }

      this.cdpInputSeq++
      this.cdpInputOutbox.push({ seq: this.cdpInputSeq, body: { ...body }, attempts: 0 })
      if (this.cdpInputOutbox.length > this.CDP_OUTBOX_MAX) {
        const dropped = this.cdpInputOutbox.shift()
        console.warn('[CDP] /cdp/input outbox overflow, dropped seq', dropped?.seq)
      }
      this.drainCDPInputOutbox()
    }

    private drainCDPInputOutbox() {
      if (this.cdpInputInFlight) return
      const head = this.cdpInputOutbox[0]
      if (!head) return
      this.cdpInputInFlight = true

      const ctrl = new AbortController()
      const timeoutId = window.setTimeout(
        () => ctrl.abort(),
        this.CDP_REQUEST_TIMEOUT_MS,
      )

      fetch(this.getCDPInputUrl(), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(head.body),
        keepalive: true,
        signal: ctrl.signal,
      }).then((res) => {
        window.clearTimeout(timeoutId)
        if (res.ok) {
          this.cdpInputOutbox.shift()
          this.cdpInputInFlight = false
          this.drainCDPInputOutbox()
          return
        }
        // 4xx is a permanent client error — retrying won't help, drop.
        if (res.status >= 400 && res.status < 500) {
          console.warn('[CDP] /cdp/input', res.status, 'seq', head.seq, '— dropping')
          this.cdpInputOutbox.shift()
          this.cdpInputInFlight = false
          this.drainCDPInputOutbox()
          return
        }
        // 5xx — server hiccup, retry with backoff.
        this.retryCDPInput(head)
      }).catch((err) => {
        window.clearTimeout(timeoutId)
        // Network error / timeout / abort — retry with backoff.
        console.warn('[CDP] /cdp/input attempt', head.attempts + 1, 'failed:', err?.message || err)
        this.retryCDPInput(head)
      })
    }

    private retryCDPInput(head: { seq: number; attempts: number }) {
      head.attempts++
      if (head.attempts >= this.CDP_MAX_ATTEMPTS) {
        console.warn('[CDP] /cdp/input giving up seq', head.seq, 'after', head.attempts, 'attempts')
        this.cdpInputOutbox.shift()
        this.cdpInputInFlight = false
        this.drainCDPInputOutbox()
        return
      }
      // Exponential backoff: 250ms, 500ms, 1s, 2s, capped at 4s.
      const delay = Math.min(250 * (1 << (head.attempts - 1)), 4000)
      this.cdpInputInFlight = false
      window.setTimeout(() => this.drainCDPInputOutbox(), delay)
    }

    // Insert plain text via the server-side persistent CDP session. Full
    // Unicode passthrough; batches multiple chars into one request when the
    // IME hands us a chunk (autocorrect replacement, voice input, swipe-type).
    private sendCDPText(text: string) {
      if (!text) return
      this.postCDPInput({ text })
    }

    // Dispatch one or more synthetic special keys. `count` lets the IME
    // backspace-loop logic flush N deletes in a single request instead of N
    // round-trips.
    private sendCDPSpecialKey(name: 'Backspace' | 'Enter' | 'Tab', count: number = 1) {
      if (count <= 0) return
      this.postCDPInput({ key: name, count })
    }

    // Active-element fetch state: deduplicates concurrent callers and caches
    // the last-good result for a short TTL. Each tap fires three logical
    // callers (touchstart prefetch, touchend post-tap check, select bridge);
    // on a 3 s RTT link three independent fetches all race, the wrong one
    // wins, and the keyboard pops on non-input taps. Sharing one in-flight
    // promise and reusing its result for ~750 ms collapses those three into
    // a single round-trip per tap.
    private cdpFocusCache: ActiveElementInfo | null = null
    private cdpFocusCacheAt = 0
    private cdpFocusInFlight: Promise<ActiveElementInfo | null> | null = null
    // When the WS is connected, focus snapshots stream in every ~200 ms, so
    // any cached value is at most a tick stale. Long TTL lets tap handlers
    // skip the HTTP fetch entirely. If the WS is down, the next /cdp/active-
    // element call goes out and refreshes naturally on the slow path.
    private readonly CDP_FOCUS_TTL_MS = 5000

    async checkElementHasFocus(): Promise<ActiveElementInfo | null> {
      // 1) Recent cached result — return synchronously.
      if (this.cdpFocusCache && Date.now() - this.cdpFocusCacheAt < this.CDP_FOCUS_TTL_MS) {
        return this.cdpFocusCache
      }
      // 2) In-flight fetch — every concurrent caller awaits the same promise.
      if (this.cdpFocusInFlight) {
        return this.cdpFocusInFlight
      }
      // 3) Fire a fresh one.
      this.cdpFocusInFlight = (async () => {
        try {
          const res = await fetch(this.getActiveElementUrl())
          if (!res.ok) return null
          const data = await res.json()
          const info: ActiveElementInfo = {
            isInput: !!data.isInput,
            tag: data.tag || 'unknown',
            type: data.type,
            isEditable: data.isEditable,
            readonly: !!data.readonly,
            disabled: !!data.disabled,
            focusKey: data.focusKey,
            elementTop: data.elementTop,
            elementHeight: data.elementHeight,
            elementLeft: data.elementLeft,
            elementWidth: data.elementWidth,
            selectInfo: data.selectInfo,
          }
          this.cdpFocusCache = info
          this.cdpFocusCacheAt = Date.now()
          return info
        } catch {
          return null
        } finally {
          this.cdpFocusInFlight = null
        }
      })()
      return this.cdpFocusInFlight
    }

    // ──────────────────────────────────────────────────────────────────
    // Select-dropdown bridge: when a remote <select> gains focus, surface
    // it to the embedding portal so the portal can render its own dropdown
    // UI in place of Chromium's native (stream-invisible) one.
    // ──────────────────────────────────────────────────────────────────

    // Track the focusKey of the select we last announced via POPCORN_SHOW_SELECT
    // so we don't spam the portal with duplicate messages while the user is
    // still interacting with the same dropdown.
    private lastAnnouncedSelectKey: string | null = null

    // Post-tap select detection. Called from the touch tap handler ~50ms after
    // the click is dispatched (gives chromium time to focus the select).
    async maybeAnnounceSelect() {
      const info = await this.checkElementHasFocus()
      if (!info?.selectInfo) {
        this.lastAnnouncedSelectKey = null
        return
      }
      if (info.focusKey && info.focusKey === this.lastAnnouncedSelectKey) return
      this.lastAnnouncedSelectKey = info.focusKey || null
      try {
        window.parent.postMessage({
          type: 'POPCORN_SHOW_SELECT',
          rect: info.selectInfo.rect,
          multiple: info.selectInfo.multiple,
          options: info.selectInfo.options,
        }, '*')
      } catch { /* no parent or cross-origin */ }
    }

    // Portal → popcorn message handler. The portal dispatches its custom
    // dropdown UI and calls back with the user's selection (or a close).
    onPortalMessage = (e: MessageEvent) => {
      const data = e.data
      if (!data || typeof data !== 'object') return
      switch (data.type) {
        case 'PORTAL_SET_SELECT_VALUE':
          this.applySelectValue(Array.isArray(data.values) ? data.values : []).catch((err) => {
            console.warn('[POPCORN] PORTAL_SET_SELECT_VALUE failed', err)
          })
          break
        case 'PORTAL_CLOSE_SELECT':
          // Just clear the announce-debounce key. We don't actively blur the
          // remote select — Runtime.evaluate isn't on the WSS allowlist, and
          // a follow-up tap will naturally move focus elsewhere. If we ever
          // need explicit blur, add it to /cdp/set-select-value or a sibling.
          this.lastAnnouncedSelectKey = null
          break
      }
    }

    // Apply user-selected values to the currently focused remote <select>.
    // For single-select, `values` is a one-element array.
    //
    // Goes via the server-side POST /cdp/set-select-value endpoint (matching
    // the /cdp/active-element + /cdp/emulate-device pattern) rather than
    // sending Runtime.evaluate over the WSS proxy directly. The WSS proxy at
    // :9222 deliberately doesn't allow Runtime.evaluate — that would let any
    // caller reachable on the port execute arbitrary JS in the page. The
    // server-side endpoint uses the unfiltered upstream CDP socket and
    // exposes the typed `{values}` request as the only knob.
    private getSetSelectValueUrl(): string {
      const params = new URLSearchParams(window.location.search)
      const sessionId = params.get('session_id')
      const token = params.get('token')
      if (sessionId && token) {
        return `${window.location.origin}/api/${sessionId}/${token}/cdp/set-select-value`
      }
      return this.resolveCDPUrl('set-select-value')
    }

    async applySelectValue(values: string[]) {
      try {
        const res = await fetch(this.getSetSelectValueUrl(), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ values }),
        })
        if (!res.ok) {
          console.warn('[POPCORN] set-select-value returned', res.status)
        }
      } catch (err) {
        console.warn('[POPCORN] set-select-value failed', err)
      } finally {
        this.lastAnnouncedSelectKey = null
      }
    }

    /**
     * Synchronous check using XMLHttpRequest.
     * Blocks the main thread until the response arrives, which is acceptable
     * here because: (a) it's hitting localhost (~1-5ms), and (b) we MUST know
     * the result before deciding to focus (Safari keyboard limitation).
     */
    checkElementHasFocusSync(): {
      isInput: boolean
      tag: string
      rawOuterHTML?: string
      type?: string
      isEditable?: boolean
      readonly?: boolean
      disabled?: boolean
      elementTop?: number
      elementHeight?: number
      elementLeft?: number
      elementWidth?: number
    } {
      const url = this.getActiveElementUrl();
      try {
        const xhr = new XMLHttpRequest();
        xhr.open('GET', url, false); // false = synchronous
        xhr.send();
        if (xhr.status === 200) {
          const data = JSON.parse(xhr.responseText);
          console.log('[CDP] active-element result (sync):', data);
          return {
            isInput: !!data.isInput,
            tag: data.tag || 'unknown',
            type: data.type,
            isEditable: data.isEditable,
            rawOuterHTML: data.rawOuterHTML,
            readonly: !!data.readonly,
            disabled: !!data.disabled,
            elementTop: data.elementTop,
            elementHeight: data.elementHeight,
            elementLeft: data.elementLeft,
            elementWidth: data.elementWidth,
          };
        }
        console.error('[CDP] active-element sync non-200:', xhr.status);
        return { isInput: true, tag: 'fetch-error' };
      } catch (e) {
        console.error('[CDP] active-element sync error:', e);
        // Optimistic default: keep keyboard up on error
        return { isInput: true, tag: 'fetch-error' };
      }
    }    // --- End CDP Logic ---


    async play() {
      if (!this._video.paused || !this.playable) {
        return
      }

      try {
        await this._video.play()
        this.onResize()
      } catch (err: any) {
        this.$log.error(err)
      }
    }

    pause() {
      if (this._video.paused || !this.playable) {
        return
      }

      this._video.pause()
    }

    toggle() {
      if (!this.playable) {
        return
      }

      if (!this.playing) {
        this.$accessor.video.play()
      } else {
        this.$accessor.video.pause()
      }
    }

    playAndUnmute() {
      this.$accessor.video.play()
      this.$accessor.video.setMuted(false)
    }

    unmute() {
      this.$accessor.video.setMuted(false)
    }

    toggleControl() {
      if (!this.playable) {
        return
      }

      this.$accessor.remote.toggle()
    }

    requestControl() {
      this.$accessor.remote.request()
    }

    requestFullscreen() {
      // try to fullscreen player element
      if (elementRequestFullscreen(this._player)) {
        this.onResize()
        return
      }

      // fallback to fullscreen video itself (on mobile devices)
      if (elementRequestFullscreen(this._video)) {
        this.onResize()
        return
      }
    }

    requestPictureInPicture() {
      //@ts-ignore
      this._video.requestPictureInPicture()
      this.onResize()
    }

    openResolution(event: MouseEvent) {
      this._resolution.open(event)
    }

    openClipboard() {
      this._clipboard.open()
    }

    async syncClipboard() {
      if (this.clipboard_read_available && window.document.hasFocus()) {
        try {
          const text = await navigator.clipboard.readText()
          if (this.clipboard !== text) {
            this.$accessor.remote.setClipboard(text)
            this.$accessor.remote.sendClipboard(text)
          }
        } catch (err: any) {
          this.$log.error(err)
        }
      }
    }

    sendMousePos(e: MouseEvent) {
      const rect = this._overlay.getBoundingClientRect()
      // In CSS-crop mode (mobileViewportActive), object-fit:none +
      // object-position:0 0 maps one client pixel to one framebuffer
      // pixel to one remote-viewport pixel — strict 1:1, NO scaling.
      // In non-crop mode the video is scaled to fit the overlay, so we
      // map client px → remote-viewport px via the server-pushed layout
      // viewport (or the stream resolution as fallback).
      const vw = this.cdpFocusCache?.viewportWidth
      const vh = this.cdpFocusCache?.viewportHeight
      const w = this.mobileViewportActive
        ? rect.width
        : (vw && vw > 0 ? vw : this.$accessor.video.resolution.w)
      const h = this.mobileViewportActive
        ? rect.height
        : (vh && vh > 0 ? vh : this.$accessor.video.resolution.h)

      this.$client.sendData('mousemove', {
        x: Math.round((w / rect.width) * (e.clientX - rect.left)),
        y: Math.round((h / rect.height) * (e.clientY - rect.top)),
      })
    }

    wheelThrottle = false
    onWheel(e: WheelEvent) {
      if (!this.hosting || this.locked) {
        return
      }

      let x = e.deltaX
      let y = e.deltaY

      // Normalize to pixel units. deltaMode 1 = lines, 2 = pages; convert
      // both to approximate pixel values so the divisor below works uniformly.
      if (e.deltaMode !== 0) {
        x *= WHEEL_LINE_HEIGHT
        y *= WHEEL_LINE_HEIGHT
      }

      if (this.scroll_invert) {
        x = x * -1
        y = y * -1
      }

      // The server sends one XTestFakeButtonEvent per unit we pass here,
      // and each event scrolls Chromium by ~120 px. Raw pixel deltas from
      // trackpads are already in pixels (~120 per notch), so dividing by
      // PIXELS_PER_TICK converts them to discrete scroll "ticks". The
      // result is clamped to [-scroll, scroll] (the user-facing sensitivity
      // setting) so fast swipes don't over-scroll.
      const PIXELS_PER_TICK = 120
      x = x === 0 ? 0 : Math.min(Math.max(Math.round(x / PIXELS_PER_TICK) || Math.sign(x), -this.scroll), this.scroll)
      y = y === 0 ? 0 : Math.min(Math.max(Math.round(y / PIXELS_PER_TICK) || Math.sign(y), -this.scroll), this.scroll)

      this.sendMousePos(e)

      if (!this.wheelThrottle) {
        this.wheelThrottle = true
        this.$client.sendData('wheel', { x, y })
        this.lastScrollAt = Date.now()

        window.setTimeout(() => {
          this.wheelThrottle = false
        }, 100)
      }
    }

    onTouchHandler(e: TouchEvent) {
      if (!this.hosting || this.locked) return

      // Multi-touch: cancel any pending single-touch state; pinch handler (if/when
      // wired through CDP) takes over here. For now we just bail so the single-
      // finger machine doesn't mis-interpret two fingers as a fast swipe.
      if (e.touches.length > 1) {
        this.cancelLongPress()
        this.touchMode = 'idle'
        return
      }

      const touch = e.changedTouches[0]
      if (!touch) return

      switch (e.type) {
        case 'touchstart': {
          this.touchMode = 'pending'
          this.touchStartX = touch.clientX
          this.touchStartY = touch.clientY
          this.touchStartTime = Date.now()
          this.touchLastX = touch.clientX
          this.touchLastY = touch.clientY
          this.touchLastWheelEmit = 0
          this.scrollAccumX = 0
          this.scrollAccumY = 0

          // Mobile-viewport mode: dispatch a real touchstart over the
          // data channel. Chromium gets a native TouchEvent, which is
          // what mobile-only captchas (Cloudflare Turnstile press-and-
          // hold, hCaptcha, reCAPTCHA "slide to verify") look for.
          if (this.mobileViewportActive) {
            this.sendTouchFromTouch(touch, 'touchstart')
          }

          // iOS: kick off the focus-state fetch now so it's in flight while
          // the user is still completing the tap. Skipped on Android — the
          // Android branch already runs async 150 ms after touchend.
          if (this.isIOS && this.is_touch_device && !this.keyboardJustDismissed) {
            this.touchstartFocusResult = undefined
            this.checkElementHasFocus()
              .then((info) => { this.touchstartFocusResult = info })
              .catch(() => { this.touchstartFocusResult = null })
          }

          this.cancelLongPress()
          this.longPressTimer = window.setTimeout(() => {
            if (this.touchMode !== 'pending') return
            this.touchMode = 'longpress'
            this.sendMousePosFromTouch(touch)
            this.$client.sendData('mousedown', { key: 3 })
          }, 500)
          break
        }

        case 'touchmove': {
          if (this.touchMode === 'idle' || this.touchMode === 'longpress') return

          const dx = touch.clientX - this.touchStartX
          const dy = touch.clientY - this.touchStartY

          // Promote pending → scroll once the finger has clearly moved.
          if (this.touchMode === 'pending') {
            if (Math.hypot(dx, dy) <= 8) return
            this.touchMode = 'scroll'
            this.cancelLongPress()
          }

          // Scroll via wheel events for ALL touch scrolling (mobile, desktop,
          // popups). The native-touch scroll path (TOUCH_UPDATE → touchscreen
          // driver) was unreliable and inverted on this neko build, so it is no
          // longer used for scrolling; the pointer-scroll path is reliable,
          // correctly-directed, and reaches popup windows. (Tap→click and
          // long-press still emit touch/mouse from touchstart/touchend.)
          // Throttle to ~frame rate FIRST, and advance touchLast only when we
          // actually emit. The old order updated touchLast on every touchmove
          // and only THEN checked the throttle — so a dropped sub-16ms frame's
          // finger distance was discarded, losing a big fraction of the swipe on
          // 60Hz+ touch streams (scroll felt far too slow). Now a dropped frame's
          // distance rolls into the next emit's delta.
          const now = Date.now()
          if (now - this.touchLastWheelEmit < 16) return
          this.touchLastWheelEmit = now

          const moveDx = touch.clientX - this.touchLastX
          const moveDy = touch.clientY - this.touchLastY
          this.touchLastX = touch.clientX
          this.touchLastY = touch.clientY

          // Emit the real touch move too, so the remote sees a balanced
          // touchstart→move→end gesture (a stray begin+end would register as a
          // tap) and touch-driven captcha/slider drags keep working. The touch
          // driver doesn't itself scroll on this build, so this does NOT double
          // up with the scroll below.
          if (this.mobileViewportActive) {
            this.sendTouchFromTouch(touch, 'touchmove')
          }

          // Preferred path: pixel-precise smooth scroll over CDP. Sends the
          // finger delta as Input.dispatchMouseEvent{mouseWheel} on the active
          // target, scrolling the element under the finger sub-notch-smoothly
          // and reaching popups. Direction: finger up (moveDy<0) → page down →
          // positive deltaY, so delta = -move. 1:1 with the finger (the CDP
          // delta is in pixels, so no PX_PER_TICK divisor / accumulator needed).
          const remote = this.touchToRemoteCoords(touch)
          if (this.sendCDPScroll(remote.x, remote.y, -moveDx * TOUCH_SCROLL_GAIN, -moveDy * TOUCH_SCROLL_GAIN)) {
            this.lastScrollAt = Date.now()
            break
          }

          // Fallback (CDP WS down): notchy neko XTest wheel.
          // Pixel-proportional scroll, native mobile direction: swiping the
          // finger UP moves the page DOWN (you pull content up to see what's
          // below). On this remote that means following the finger delta with
          // NO sign flip — moveDy<0 (finger up) → negative wheelY → page scrolls
          // down. Accumulate the fractional remainder so slow, precise drags
          // aren't rounded away to zero (felt like dead/janky scrolling).
          this.scrollAccumX += moveDx / TOUCH_SCROLL_PX_PER_TICK
          this.scrollAccumY += moveDy / TOUCH_SCROLL_PX_PER_TICK

          // Bad-network backpressure: the input data channel is reliable+ordered,
          // so on a congested link emitting at 60/s just builds an SCTP backlog —
          // scroll lags and overshoots after the finger lifts. If the send buffer
          // is backing up, hold: keep the delta in the accumulator and flush it as
          // one larger wheel event once the channel drains. Bounds latency, never
          // loses scroll distance.
          if (this.$client.dataBufferedAmount > SCROLL_MAX_BUFFERED_BYTES) {
            break
          }

          let wheelX = Math.trunc(this.scrollAccumX)
          let wheelY = Math.trunc(this.scrollAccumY)
          this.scrollAccumX -= wheelX
          this.scrollAccumY -= wheelY
          // Clamp per-event so one fast flick (or a post-backpressure flush)
          // doesn't teleport the page.
          wheelX = Math.min(Math.max(wheelX, -this.scroll), this.scroll)
          wheelY = Math.min(Math.max(wheelY, -this.scroll), this.scroll)

          if (wheelX || wheelY) {
            this.sendMousePosFromTouch(touch)
            this.$client.sendData('wheel', { x: wheelX, y: wheelY })
            this.lastScrollAt = Date.now()
          }
          break
        }

        case 'touchend':
        case 'touchcancel': {
          this.cancelLongPress()
          const mode = this.touchMode
          const duration = Date.now() - this.touchStartTime
          this.touchMode = 'idle'

          // Always send a touchend if we sent a touchstart on this gesture
          // (mobile mode). Even for cancels — chromium expects touch
          // events to balance, else its internal touch-tracking leaks.
          if (this.mobileViewportActive) {
            this.sendTouchFromTouch(touch, 'touchend')
          }

          if (e.type === 'touchcancel') return

          if (mode === 'pending' && duration < 300) {
            // Tap: always synthesize the mouse click — this is what
            // makes taps work on neko builds that don't yet handle the
            // new touch opcodes. The touchstart/touchend pair we sent
            // earlier (if mobileViewportActive) is purely additive so
            // captcha-style pages can see real TouchEvents.
            this.sendMousePosFromTouch(touch)
            this.$client.sendData('mousedown', { key: 1 })
            this.$client.sendData('mouseup', { key: 1 })

            if (this.is_touch_device && !this.keyboardJustDismissed) {
              const remote = this.touchToRemoteCoords(touch)
              // Synchronous tap-time decision using the input-rects cache.
              // O(N) local lookup, no network round-trip — identical
              // behavior at 50 ms or 5 s RTT.
              const hit = this.hitTestInputRect(remote.x, remote.y)
              if (hit === 'hit') {
                if (!this.keyboardActive) {
                  this.focusProxyInputForIOS()
                } else if (this.isAndroid && this._proxyInput) {
                  // Already up — clear the proxy's buffered text so the next
                  // keystroke doesn't merge with the previous field's word,
                  // then re-focus to nudge Samsung Keyboard if it minimized.
                  this._proxyInput.value = ''
                  this._proxyInput.blur()
                  this._proxyInput.focus()
                }
              } else if (hit === 'miss' && this.keyboardActive) {
                this.dismissKeyboard('tap-miss')
              }
              // hit === 'unknown' (cache cold): leave keyboard state alone.
              // The user can use the toolbar keyboard icon, and any focus
              // event the remote eventually pushes via WS will pop or
              // dismiss correctly via onFocusSnapshotUpdated.

              // Mark this tap for the post-tap stuck-state poller / select
              // bridge. Record the pre-tap focusKey so the focus-event
              // watcher can detect re-taps on the same input.
              this.pendingTapAt = Date.now()
              this.pendingTapPreFocusKey = this.cdpFocusCache?.focusKey || null
              // If the tap landed on a <select>, surface it to the embedding
              // portal so it can render its own dropdown. Delayed a tick to
              // give chromium time to set document.activeElement to the select.
              window.setTimeout(() => {
                this.maybeAnnounceSelect().catch(() => { /* best-effort */ })
              }, 60)
            }
          } else if (mode === 'longpress') {
            this.$client.sendData('mouseup', { key: 3 })
          }
          // mode === 'scroll' or stale 'pending' past tap timeout: nothing to do.
          break
        }
      }
    }

    private cancelLongPress() {
      if (this.longPressTimer !== null) {
        window.clearTimeout(this.longPressTimer)
        this.longPressTimer = null
      }
    }

    private sendMousePosFromTouch(t: Touch) {
      const { x, y } = this.touchToRemoteCoords(t)
      this.$client.sendData('mousemove', { x, y })
    }

    // Mirror the focused remote input's IME-shaping attributes onto our
    // proxy. Called from focusProxyInputForIOS (when popping the kbd)
    // and from onFocusSnapshotUpdated (when focus transitions on the
    // remote while the kbd is already up). Falls back to plain text +
    // off-autocomplete when the remote isn't on an input.
    private applyProxyImeHints() {
      const proxy = this._proxyInput
      if (!proxy) return
      const info = this.cdpFocusCache
      const tag = (info?.tag || '').toLowerCase()
      const remoteInputType = (info?.inputType || '').toLowerCase()
      const remoteInputMode = (info?.inputMode || '').toLowerCase()
      const remoteAC = info?.autoComplete || ''
      const remoteEKH = (info?.enterKeyHint || '').toLowerCase()

      // type: prefer remote's type for <input>. textarea / contenteditable
      // stay 'text' (HTML <input> doesn't support type=textarea).
      let proxyType = 'text'
      if (tag === 'input' && remoteInputType) {
        // Only forward types the IME treats specially. Filter unsafe ones
        // (file/submit/checkbox/etc.) — those would change <input>
        // behavior in ways unrelated to the kbd layout.
        const allowed = new Set(['text', 'email', 'tel', 'url', 'password', 'search', 'number'])
        if (allowed.has(remoteInputType)) proxyType = remoteInputType
      }
      try { proxy.type = proxyType } catch { /* noop */ }

      // inputmode: overrides type for kbd layout when set.
      if (remoteInputMode) {
        try { proxy.setAttribute('inputmode', remoteInputMode) } catch { /* noop */ }
      } else {
        try { proxy.removeAttribute('inputmode') } catch { /* noop */ }
      }

      // autocomplete: 'one-time-code' triggers iOS / Gboard SMS-code
      // autofill suggestion bar. Forward all autocomplete tokens —
      // platforms know which ones they understand.
      if (remoteAC) {
        try { proxy.setAttribute('autocomplete', remoteAC) } catch { /* noop */ }
      } else {
        try { proxy.setAttribute('autocomplete', 'off') } catch { /* noop */ }
      }

      // enterkeyhint: labels the Enter key (send / search / go / done /
      // next / previous). Helps the user know what Enter will do.
      if (remoteEKH) {
        try { proxy.setAttribute('enterkeyhint', remoteEKH) } catch { /* noop */ }
      } else {
        try { proxy.removeAttribute('enterkeyhint') } catch { /* noop */ }
      }
    }

    // Dispatch a real touch event over the neko data channel. The fork's
    // xf86-input-neko driver already registers as XI_TOUCHSCREEN with up
    // to 10 slots; on the server side this opcode forwards to that
    // driver's touch slot API and chromium sees a native TouchEvent.
    // Used in mobile-viewport mode for taps, scrolls, and captcha
    // gestures — pages that listen for touchstart/move/end (almost every
    // modern mobile site) get the real thing instead of synthesized
    // mouse events. Slot id is fixed to 0 for single-finger; multi-touch
    // (pinch zoom etc.) would extend this to track per-touch ids.
    private sendTouchFromTouch(t: Touch, event: 'touchstart' | 'touchmove' | 'touchend') {
      const { x, y } = this.touchToRemoteCoords(t)
      this.$client.sendData(event, { id: 0, x, y })
    }

    // Map a Touch event to the equivalent coordinate inside the remote
    // page's layout viewport. Two regimes:
    //   - CSS-crop mode (mobileViewportActive): object-fit:none + object-
    //     position:0 0 paints one client pixel per framebuffer pixel per
    //     remote-viewport pixel. 1:1 mapping, NO scaling.
    //   - Scaled mode (full-page video): map via the server-pushed remote
    //     viewport (or fall back to the stream resolution).
    private touchToRemoteCoords(t: Touch): { x: number; y: number } {
      const rect = this._overlay.getBoundingClientRect()
      const vw = this.cdpFocusCache?.viewportWidth
      const vh = this.cdpFocusCache?.viewportHeight
      const w = this.mobileViewportActive
        ? rect.width
        : (vw && vw > 0 ? vw : this.width)
      const h = this.mobileViewportActive
        ? rect.height
        : (vh && vh > 0 ? vh : this.height)
      return {
        x: Math.round((w / rect.width) * (t.clientX - rect.left)),
        y: Math.round((h / rect.height) * (t.clientY - rect.top)),
      }
    }

    // Tier-1 sync hit-test. Returns 'hit' / 'miss' / 'unknown'. 'unknown'
    // means the cache hasn't received its first push yet (page just loaded,
    // WS not connected, or 3 s RTT before the first push arrives). Caller
    // treats 'unknown' as "leave keyboard state alone" — neither pop nor
    // dismiss — so we never misfire on a cold cache.
    // Most recent hit-test verdict, used by the debug overlay to surface
    // what the system decided.
    private lastHitResult: 'hit' | 'miss' | 'unknown' = 'unknown'

    private hitTestInputRect(x: number, y: number): 'hit' | 'miss' | 'unknown' {
      const result = this._hitTestInputRect(x, y)
      this.lastHitResult = result
      if (this.debugRects) {
        console.log('[hit-test]', { x, y, result, rects: this.inputRectsCache.length, ready: this.inputRectsReady, scrollAgo: Date.now() - this.lastScrollAt })
      }
      return result
    }

    private _hitTestInputRect(x: number, y: number): 'hit' | 'miss' | 'unknown' {
      if (!this.inputRectsReady) return 'unknown'
      // Recent scroll invalidates the cache (rects haven't refreshed yet
      // for the new scroll offset). Defer the tap decision rather than
      // false-positive into a dismiss while the user is mid-flick.
      if (this.lastScrollAt && Date.now() - this.lastScrollAt < this.SCROLL_CACHE_INVALID_MS) {
        return 'unknown'
      }
      const SLOP = 4 // px, accommodates antialiased edges + emulator rounding
      const rects = this.inputRectsCache
      for (let i = 0; i < rects.length; i++) {
        const r = rects[i]
        if (x >= r.x - SLOP && x <= r.x + r.width + SLOP &&
            y >= r.y - SLOP && y <= r.y + r.height + SLOP) {
          return 'hit'
        }
      }
      return 'miss'
    }

    // True iff the tap landed inside the focused element's bounding rect.
    // Used to gate keyboard popup: activeElement may still report the
    // previously-focused input after the user tapped a non-input area (clicks
    // on non-focusable regions don't blur), so just checking isInput would
    // wrongly re-pop the keyboard.
    private tapHitFocusedElement(
      info: { elementLeft?: number; elementTop?: number; elementWidth?: number; elementHeight?: number },
      remoteX: number,
      remoteY: number,
    ): boolean {
      const l = info.elementLeft, t = info.elementTop
      const w = info.elementWidth, h = info.elementHeight
      if (l === undefined || t === undefined || !w || !h) return false
      return remoteX >= l && remoteX <= l + w && remoteY >= t && remoteY <= t + h
    }

    // iOS Safari autocomplete, Android Gboard swipe-typing, and CJK IMEs deliver
    // text via `beforeinput`/`input` events with no usable keydown. Translate
    // each input into Unicode keysym keydown+keyup pairs so the remote server
    // sees them as real keystrokes.
    //
    // The composition guard is critical for Android: Gboard fires a stream of
    // `beforeinput` events with cumulative `data` ("h", "he", "hel", "hell",
    // "hello") during predictive typing. We let the textarea accumulate during
    // composition and forward the final text once via `onCompositionEnd`.
    // SwiftKey auto-inserts a space after punctuation (!./,?:;'"()]}).
    // Strip those because they corrupt remote-field state — the user didn't
    // type the space. `lastCharSent` and `lastPunctuationTime` carry context
    // across event boundaries so trailing-edge cases (punctuation at the end
    // of one batch, space at the start of the next) are still caught.
    private filterAutoSpace(chars: string): string {
      const punctuationChars = new Set(['!', '.', ',', '?', ':', ';', '\'', '"', ')', ']', '}'])
      if (chars.length === 0) return chars
      const now = Date.now()
      let result = ''
      for (let i = 0; i < chars.length; i++) {
        const char = chars[i]
        const prevChar = i > 0 ? chars[i - 1] : this.lastCharSent
        const timeSincePunctuation = now - this.lastPunctuationTime
        if (char === ' ' && punctuationChars.has(prevChar) && (i > 0 || timeSincePunctuation < 100)) continue
        result += char
        if (punctuationChars.has(char)) this.lastPunctuationTime = now
      }
      if (result.length > 0) this.lastCharSent = result[result.length - 1]
      return result
    }

    // Per-platform IME handlers, ported from keyboard.ts:2977-3235.
    // Android: value-comparison in onProxyInput is the source of truth.
    //   beforeinput only handles deletion-on-empty and Enter.
    // iOS: beforeinput is the source of truth; onProxyInput is a fallback.
    onProxyBeforeInput(e: InputEvent) {
      if (!this.hosting || this.locked) return
      const proxy = this._proxyInput
      if (!proxy) return
      const inputType = e.inputType
      const data = e.data

      if (this.isAndroid) {
        if (inputType === 'deleteContentBackward' || inputType === 'deleteByCut' ||
            inputType === 'deleteContent' || inputType === 'deleteContentForward') {
          if (this.pendingBackspaceTimer !== null) {
            window.clearTimeout(this.pendingBackspaceTimer)
            this.pendingBackspaceTimer = null
          }
          if (proxy.value === '') {
            e.preventDefault()
            this.sendCDPSpecialKey('Backspace')
            return
          }
          return
        }
        // Any non-deletion input cancels the pending Unidentified backspace.
        if (this.pendingBackspaceTimer !== null) {
          window.clearTimeout(this.pendingBackspaceTimer)
          this.pendingBackspaceTimer = null
        }
        if (inputType === 'insertLineBreak') {
          e.preventDefault()
          this.sendCDPSpecialKey('Enter')
          proxy.value = ''
          this.lastSentValue = ''
        }
        return
      }

      // iOS path.
      if (inputType === 'insertText' && data) {
        // Single CDP call for the whole batch — cuts N round-trips to 1 for
        // voice input, glide-typing, and autocorrect replacements.
        this.sendCDPText(data)
        e.preventDefault()
        proxy.value = ''
      } else if (inputType === 'deleteContentBackward') {
        e.preventDefault()
        this.sendCDPSpecialKey('Backspace')
        proxy.value = ''
      } else if (inputType === 'insertLineBreak') {
        e.preventDefault()
        this.sendCDPSpecialKey('Enter')
        proxy.value = ''
      }
    }

    onProxyInput(e: InputEvent) {
      if (!this.hosting || this.locked) return
      const proxy = this._proxyInput
      if (!proxy) return
      const currentValue = proxy.value
      const inputType = e.inputType

      if (this.isAndroid) {
        if (this.pendingBackspaceTimer !== null) {
          window.clearTimeout(this.pendingBackspaceTimer)
          this.pendingBackspaceTimer = null
        }

        if (inputType === 'deleteContentBackward' || inputType === 'deleteByCut' ||
            inputType === 'deleteContent' || inputType === 'deleteContentForward') {
          this.sendCDPSpecialKey('Backspace')
          this.lastSentValue = currentValue
          return
        }

        // Value shrunk → user deleted. Emit one Backspace per missing char,
        // then re-type any divergent remainder (autocorrect mid-word delete).
        if (currentValue.length < this.lastSentValue.length) {
          const deletedCount = this.lastSentValue.length - currentValue.length
          this.sendCDPSpecialKey('Backspace', deletedCount)
          if (currentValue && !this.lastSentValue.startsWith(currentValue)) {
            this.sendCDPText(currentValue)
          }
          this.lastSentValue = currentValue
          return
        }

        if (inputType === 'insertText' || inputType === 'insertCompositionText' ||
            currentValue.length > this.lastSentValue.length) {
          if (currentValue.length > this.lastSentValue.length) {
            if (currentValue.startsWith(this.lastSentValue)) {
              // Append-only: send just the new tail. Common case for
              // single-key typing, swipe-type, and voice input.
              const newChars = currentValue.slice(this.lastSentValue.length)
              const filtered = this.filterAutoSpace(newChars)
              if (filtered) this.sendCDPText(filtered)
              if (filtered !== newChars) {
                proxy.value = ''
                this.lastSentValue = ''
                return
              }
            } else {
              // Autocorrect mid-word: delete old, retype new.
              this.sendCDPSpecialKey('Backspace', this.lastSentValue.length)
              const filtered = this.filterAutoSpace(currentValue)
              if (filtered) this.sendCDPText(filtered)
              if (filtered !== currentValue) {
                proxy.value = ''
                this.lastSentValue = ''
                return
              }
            }
          } else if (currentValue !== this.lastSentValue && currentValue.length > 0) {
            // Same length, different content (whole-word autocorrect).
            this.sendCDPSpecialKey('Backspace', this.lastSentValue.length)
            const filtered = this.filterAutoSpace(currentValue)
            if (filtered) this.sendCDPText(filtered)
            if (filtered !== currentValue) {
              proxy.value = ''
              this.lastSentValue = ''
              return
            }
          }
        }

        this.lastSentValue = currentValue
        return
      }

      // iOS / non-Android fallback. Most input is already handled in
      // onProxyBeforeInput above; if anything sneaks through (some Safari
      // versions don't fire beforeinput for autocomplete), forward it here.
      if (currentValue) {
        this.sendCDPText(currentValue)
      }
      proxy.value = ''
    }

    onProxyKeyDown(e: KeyboardEvent) {
      if (!this.hosting || this.locked) return
      const proxy = this._proxyInput
      if (!proxy) return

      if (this.isAndroid) {
        switch (e.key) {
          case 'Backspace':
            // Empty proxy → no input event will fire to signal deletion.
            // Send Backspace directly.
            if (proxy.value === '') {
              e.preventDefault()
              this.sendCDPSpecialKey('Backspace')
            }
            return
          case 'Unidentified':
            // Ridmik and other Indic IMEs fire keydown key='Unidentified' for
            // every key including backspace. Defer ~80ms; if a beforeinput or
            // input event follows, it was a character — the timer is cleared
            // there. Otherwise it was a backspace on empty proxy and we fire.
            if (proxy.value === '') {
              if (this.pendingBackspaceTimer !== null) window.clearTimeout(this.pendingBackspaceTimer)
              this.pendingBackspaceTimer = window.setTimeout(() => {
                this.pendingBackspaceTimer = null
                this.sendCDPSpecialKey('Backspace')
              }, 80)
            }
            return
          case 'Enter':
            e.preventDefault(); this.sendCDPSpecialKey('Enter'); proxy.value = ''; this.lastSentValue = ''; return
          case 'Tab':
            e.preventDefault(); this.sendCDPSpecialKey('Tab'); proxy.value = ''; this.lastSentValue = ''; return
          case 'Escape':
            e.preventDefault(); this.toggleMobileKeyboard(); return
        }
        return
      }

      // iOS / non-Android.
      switch (e.key) {
        case 'Backspace': e.preventDefault(); this.sendCDPSpecialKey('Backspace'); proxy.value = ''; return
        case 'Enter':     e.preventDefault(); this.sendCDPSpecialKey('Enter');     proxy.value = ''; return
        case 'Tab':       e.preventDefault(); this.sendCDPSpecialKey('Tab');       proxy.value = ''; return
        case 'Escape':    e.preventDefault(); this.toggleMobileKeyboard();         return
      }
    }

    onProxyBlur() {
      // When the user dismissed the keyboard via the system back button or
      // Gboard's swipe-down gesture (not via our own blur flow which sets
      // allowBlur=true first), the proxy blurs without our state knowing.
      // Flip keyboardActive=false here so subsequent tap decisions don't
      // think the kbd is still up — and record a 100ms grace window so a
      // touchend that immediately follows doesn't re-pop the kbd.
      if (this.keyboardActive && !this.allowBlur && !document.hidden) {
        this.keyboardActive = false
        this.stopStuckStatePoller()
        this.keyboardJustDismissed = true
        window.setTimeout(() => { this.keyboardJustDismissed = false }, 100)
      }
    }

    onCompositionStart() {
      this.isComposing = true
    }

    onCompositionEnd(e: CompositionEvent) {
      this.isComposing = false
      if (this.isAndroid) {
        // Android: onProxyInput already sent each insertCompositionText
        // event via value-comparison. Nothing to do here.
        return
      }
      // iOS: send the final composed text in one chunk.
      if (e.data) this.sendCDPText(e.data)
      if (this._proxyInput) this._proxyInput.value = ''
    }

    // Open the mobile soft keyboard by focusing the proxy input. iOS Safari
    // standalone needs the temp-readonly-input bridge to keep the keyboard
    // up across CDP roundtrips; iOS iframe-embedded and Android can use a
    // direct focus inside the touch gesture.
    focusProxyInputForIOS() {
      const proxy = this._proxyInput
      if (!proxy) return

      // Clear any leftover dismiss-time attributes (the Android dismiss
      // hammer briefly sets inputmode=none to force Gboard to hide; if
      // the user re-taps inside that 50 ms window we'd attach the proxy
      // with the kbd-suppress attribute still on).
      try { proxy.removeAttribute('inputmode') } catch { /* noop */ }
      try { proxy.removeAttribute('readonly') } catch { /* noop */ }

      // Shape the proxy to match the focused remote input. Platform IMEs
      // pick a layout from type / inputmode / autocomplete:
      //   type=email      → '@' key + .com shortcuts
      //   type=tel        → dial-pad
      //   type=number     → numeric keyboard
      //   inputmode=decimal → decimal pad (Stripe / numeric forms)
      //   autocomplete=one-time-code → SMS code autofill on iOS / Gboard
      //   enterkeyhint=send|go|search → action-labeled Enter key
      this.applyProxyImeHints()

      // Mark the keyboard as active synchronously. visualViewport.resize
      // is the *backup* signal — some Android browsers (Brave, some
      // Samsung firmwares) don't fire it reliably when the IME slides
      // up, leaving keyboardActive=false despite a visible kbd. Setting
      // it here is the canonical "we just opened it" marker.
      if (!this.keyboardActive) {
        this.keyboardActive = true
        this.startStuckStatePoller()
      }

      // If embedded in a parent iframe, claim iframe focus inside the gesture
      // so subsequent physical keystrokes route to this window. Async refocus
      // later wouldn't satisfy iOS's user-gesture requirement.
      if (window !== window.top) {
        try { window.focus() } catch { /* cross-origin */ }
      }

      if (this.isIOS && window === window.top) {
        // iOS standalone: temp readonly input → real proxy. Keeps the keyboard
        // up while any await/setTimeout downstream resolves.
        const tempInput = document.createElement('input')
        tempInput.style.cssText = 'position:fixed;top:50%;left:50%;width:1px;height:1px;opacity:0;font-size:16px;z-index:99999;'
        tempInput.setAttribute('readonly', 'readonly')
        document.body.appendChild(tempInput)
        tempInput.focus()

        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            tempInput.removeAttribute('readonly')
            proxy.value = ''
            this.lastSentValue = ''
            proxy.focus()
            setTimeout(() => {
              if (tempInput.parentNode) tempInput.parentNode.removeChild(tempInput)
            }, 100)
          })
        })
        return
      }

      // iOS iframe-embedded OR Android: direct focus inside the gesture.
      this.keyboardOpening = true
      proxy.value = ''
      this.lastSentValue = ''
      proxy.focus()
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          if (this._proxyInput) this._proxyInput.focus()
          window.setTimeout(() => { this.keyboardOpening = false }, 500)
        })
      })
    }

    // Toggle the mobile soft keyboard. Called from the Escape key in the IME
    // handler and from external triggers (e.g., a button in extra-controls).
    toggleMobileKeyboard() {
      if (this.keyboardActive) {
        this.allowBlur = true
        if (this._proxyInput) this._proxyInput.blur()
        this.iframeOffset = 0
        this.restoreViewportAfterKeyboard()
        window.setTimeout(() => { this.allowBlur = false }, 100)
      } else {
        this.focusProxyInputForIOS()
      }
    }

    // visualViewport detector. The viewport shrinks when the soft keyboard
    // appears and grows back when it's dismissed. We require an explicit
    // "I saw it shrink" observation before treating a grow event as a
    // dismissal — otherwise spurious resize events (desktop responsive mode,
    // iframe-embedded with no virtual keyboard) would nuke our keyboard
    // state every time and kill the proxy focus mid-typing.
    handleViewportResize = () => {
      if (!window.visualViewport) return
      const viewportHeight = window.visualViewport.height
      const windowHeight = window.innerHeight

      const shrunk = (windowHeight - viewportHeight) > 50
      if (shrunk) {
        // ALWAYS update keyboardActive on shrink — even mid-opening. The
        // previous "if (keyboardOpening) return" at the top of this handler
        // blocked the resize events that fire while the kbd is animating
        // up, so keyboardActive stayed false even after the kbd was fully
        // visible. Now we only guard the *dismiss* branch with
        // keyboardOpening (further down).
        this.lastViewportShrink = true
        const wasActive = this.keyboardActive
        this.keyboardActive = true
        if (!wasActive) this.startStuckStatePoller()
        // Shrink the remote viewport to the visible area so the focused field
        // scrolls above the keyboard and occluded content stays reachable.
        this.applyKeyboardViewport(viewportHeight)
        // Notify any embedding portal so it can mirror our keyboard state.
        try {
          window.parent.postMessage({
            type: 'POPCORN_VIEWPORT',
            visibleHeight: viewportHeight,
            occludedBottom: windowHeight - viewportHeight,
          }, '*')
        } catch { /* no parent */ }
        return
      }

      if (!this.lastViewportShrink) return
      this.lastViewportShrink = false

      // Don't fire dismiss during the kbd-open animation — iOS Safari
      // briefly reports "viewport restored" mid-open before the kbd lands.
      // Only the dismiss branch gets this guard; the activation branch
      // above ran unconditionally so we caught the actual shrink.
      if (this.keyboardOpening) return

      // iOS sometimes fires a transient resize during the keyboard show
      // animation that looks like "viewport restored" — guard via active
      // element identity. If our proxy is still focused, the keyboard
      // is actually still up.
      const proxyStillFocused = document.activeElement === this._proxyInput
      if (Math.abs(viewportHeight - windowHeight) < 50 && this.keyboardActive && !proxyStillFocused) {
        this.keyboardActive = false
        this.stopStuckStatePoller()
        this.iframeOffset = 0
        window.scrollTo(0, 0)
        this.restoreViewportAfterKeyboard()
        this.keyboardJustDismissed = true
        window.setTimeout(() => { this.keyboardJustDismissed = false }, 100)
        if (this._proxyInput) this._proxyInput.blur()
        try {
          window.parent.postMessage({
            type: 'POPCORN_VIEWPORT',
            visibleHeight: viewportHeight,
            occludedBottom: 0,
          }, '*')
        } catch { /* no parent */ }
      }
    }

    onMouseDown(e: MouseEvent) {
      if (!this.hosting) {
        this.$emit('control-attempt', e)
      }

      if (!this.hosting || this.locked) {
        return
      }

      this.sendMousePos(e)
      this.$client.sendData('mousedown', { key: e.button + 1 })
    }

    onMouseUp(e: MouseEvent) {
      if (!this.hosting || this.locked) {
        return
      }

      this.sendMousePos(e)
      this.$client.sendData('mouseup', { key: e.button + 1 })
    }

    onMouseMove(e: MouseEvent) {
      if (!this.hosting || this.locked) {
        return
      }

      this.sendMousePos(e)
    }

    onMouseEnter(e: MouseEvent) {
      if (this.hosting) {
        this.$accessor.remote.syncKeyboardModifierState({
          capsLock: e.getModifierState('CapsLock'),
          numLock: e.getModifierState('NumLock'),
          scrollLock: e.getModifierState('ScrollLock'),
        })

        this.syncClipboard()
      }

      this.focused = true
    }

    onMouseLeave(e: MouseEvent) {
      if (this.hosting) {
        this.$accessor.remote.setKeyboardModifierState({
          capsLock: e.getModifierState('CapsLock'),
          numLock: e.getModifierState('NumLock'),
          scrollLock: e.getModifierState('ScrollLock'),
        })
      }

      this.keyboard.reset()
      this.focused = false
    }

    onPaste() {
      if (this.hosting) {
        this.syncClipboard()
      }
    }

    onOverlayFocus() {
      if (this.hosting) {
        this.syncClipboard()
      }
    }

    onResize() {
      const { offsetWidth, offsetHeight } = !this.fullscreen ? this._component : document.body
      this._player.style.width = `${offsetWidth}px`
      this._player.style.height = `${offsetHeight}px`

      const aspectPreservingMaxWidth = (this.horizontal / this.vertical) * offsetHeight
      this._container.style.maxWidth = `${
        !this.fullscreen ? Math.min(this.width, aspectPreservingMaxWidth) : aspectPreservingMaxWidth
      }px`
      this._aspect.style.paddingBottom = `${(this.vertical / this.horizontal) * 100}%`

      // Re-apply mobile emulation (debounced) so rotating the phone or resizing
      // the embed re-lays-out the remote page to the new iframe box.
      this.scheduleDeviceExperience()
    }

    @Watch('focused')
    @Watch('hosting')
    @Watch('locked')
    onFocus() {
      // focus opens the keyboard on mobile
      if (this.is_touch_device) {
        return
      }

      // in order to capture key events, overlay must be focused
      if (this.focused && this.hosting && !this.locked) {
        this._overlay.focus()
      }
    }

    openMobileKeyboard() {
      // focus opens the keyboard on mobile
      this._overlay.focus()
    }
  }
</script>

<template>
  <div ref="component" class="video">
    <div ref="player" class="player">
      <div ref="container" class="player-container">
        <video ref="video" playsinline :class="{ magnified: mobileMagnified }" :style="magnifyStyle" />
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

          /* Magnify mode: the inline `videoStyle` getter sizes this element
             to the framebuffer's native pixel dimensions and applies a CSS
             scale transform that maps the *emulated* viewport into the
             *visible* container area. The container's `overflow: hidden`
             clips the parts of the framebuffer outside that emulated region.
             `object-fit: fill` ensures the streamed pixels fill the element
             box at 1:1 (no intrinsic letterbox). */
          &.magnified {
            object-fit: fill;
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
        }

        .player-aspect {
          display: block;
          padding-bottom: 56.25%;
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

    // Mobile-magnify hack: when the remote framebuffer stays at desktop size
    // but the page renders into the top-left magnifyW x magnifyH region, we
    // visually scale the video element so that region fills the iframe.
    // Coord scaling and aspect ratio are adjusted to match.
    private mobileMagnified = false
    private magnifyW = 0
    private magnifyH = 0

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

    // Android auto-focus poller state.
    // suppressedFocusKey: focusKey of the element on which the user last
    //   dismissed the keyboard. While the same element remains focused, the
    //   poller stays out of the way; cleared as soon as focus moves.
    // autoFocusPoll: setInterval handle so we can stop on destroy / disable.
    private suppressedFocusKey: string | null = null
    private autoFocusPoll: number | null = null

    // Cached regex test once on construction; the result doesn't change.
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

    // Inline style for the hidden proxy input. Fixed off-screen when keyboard
    // is inactive; moves to viewport-center when active so platform IMEs
    // (Samsung especially) attach and dispatch input events.
    get proxyInputStyle(): Record<string, string> {
      if (this.keyboardActive) {
        return {
          position: 'fixed',
          top: '50%',
          left: '50%',
          transform: 'translate(-50%, -50%)',
        }
      }
      return {
        position: 'fixed',
        top: '-9999px',
        left: '-9999px',
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
    private longPressTimer: number | null = null

    // CDP state
    private cdpSocket: WebSocket | null = null
    private pendingCDPCommands: Map<number, { resolve: Function, reject: Function }> = new Map()
    private cdpCommandId = 1
    private cdpSessionId: string | null = null

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

    get is_touch_device() {
      return (
        // detect if the device has touch support
        ('ontouchstart' in window || navigator.maxTouchPoints > 0) &&
        // the primary input mechanism includes a pointing device of
        // limited accuracy, such as a finger on a touchscreen.
        window.matchMedia('(pointer: coarse)').matches
      )
    }

    // Effective dims: what the iframe actually displays (and what taps should
    // map onto). With the mobile-magnify hack, that's the mobile region; otherwise
    // it's the full framebuffer as reported by neko.
    get effectiveW(): number {
      return this.mobileMagnified && this.magnifyW > 0 ? this.magnifyW : this.width
    }
    get effectiveH(): number {
      return this.mobileMagnified && this.magnifyH > 0 ? this.magnifyH : this.height
    }

    // Reactive container dims, updated by ResizeObserver via onResize().
    // Used by videoStyle/overlayInfo to compute the magnify scale transform.
    private containerW = 0
    private containerH = 0

    // The websdk-style viewport pipeline:
    //   * `<video>` is sized at the framebuffer's native pixel dimensions
    //     (this.width × this.height) so it represents a true 1:1 capture surface
    //   * `transform: scale(N)` maps the *emulated* viewport (what CDP told the
    //     page to render at) into the *visible* container area
    //   * `left/top` centers the visible region when it doesn't fill the
    //     container (letterbox case)
    //   * `overflow: hidden` on .player-container clips the framebuffer pixels
    //     outside the emulated region
    //
    // overlayInfo exposes scale/offset/size so future overlays (popup anchors,
    // selection rects) can be positioned against the same coordinate basis,
    // matching the `_overlayInfo` block in the websdk's useViewportManagement.
    get overlayInfo() {
      const emulatedW = this.mobileMagnified && this.magnifyW > 0 ? this.magnifyW : (this.width || 1920)
      const emulatedH = this.mobileMagnified && this.magnifyH > 0 ? this.magnifyH : (this.height || 1080)
      const visibleW = this.containerW || emulatedW
      const visibleH = this.containerH || emulatedH

      const scaleX = visibleW / emulatedW
      const scaleY = visibleH / emulatedH
      const scale = Math.min(scaleX, scaleY)
      const scaledW = emulatedW * scale
      const scaledH = emulatedH * scale
      const offsetX = (visibleW - scaledW) / 2
      const offsetY = (visibleH - scaledH) / 2

      return { emulatedW, emulatedH, visibleW, visibleH, scale, scaledW, scaledH, offsetX, offsetY }
    }

    // Inline style on the <video> element. Active only in magnify mode — when
    // off we fall back to the scoped CSS (width: 100%, height: 100%) which
    // lets the video fill the player-container conventionally.
    get magnifyStyle(): Record<string, string> | undefined {
      if (!this.mobileMagnified) return undefined
      const fbW = this.width || 1920
      const fbH = this.height || 1080
      const { scale, offsetX, offsetY } = this.overlayInfo
      return {
        width: `${fbW}px`,
        height: `${fbH}px`,
        transform: `scale(${scale})`,
        transformOrigin: '0 0',
        left: `${offsetX}px`,
        top: `${offsetY}px`,
        bottom: 'auto',
        right: 'auto',
      }
    }

    @Watch('mobileMagnified')
    @Watch('magnifyW')
    @Watch('magnifyH')
    onMagnifyChanged() {
      this.onResize()
    }

    @Watch('iframeOffset')
    onIframeOffsetChanged(offset: number) {
      if (!this._player) return
      // Lift the streamed video element so the focused remote field clears
      // the on-screen keyboard. Offset is negative when lifting.
      this._player.style.transform = offset !== 0 ? `translateY(${offset}px)` : ''
      this._player.style.transition = 'transform 0.3s ease-out'
    }

    // Pick the CDP device-emulation profile for the current viewer. Returns
    // null on non-touch devices (= no override; remote renders desktop).
    //
    // The framebuffer stays at its native size; `scale` upsamples the mobile
    // page to fill the chromium window so the streamed frame isn't a tiny
    // mobile viewport in a corner of empty wallpaper.
    private pickDeviceProfile(): {
      width: number
      height: number
      mobile: boolean
      userAgent: string
      maxTouchPoints: number
    } | null {
      if (!this.is_touch_device) return null
      const w = window.innerWidth
      const h = window.innerHeight
      const portrait = h >= w
      // iPhone-class portrait phone.
      if (portrait && w <= 500) {
        return {
          width: 390,
          height: 844,
          mobile: true,
          userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
          maxTouchPoints: 5,
        }
      }
      // Tablet portrait.
      if (portrait) {
        return {
          width: 768,
          height: 1024,
          mobile: true,
          userAgent: 'Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
          maxTouchPoints: 5,
        }
      }
      // Touch device in landscape (rotated tablet).
      return {
        width: 1024,
        height: 768,
        mobile: true,
        userAgent: 'Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
        maxTouchPoints: 5,
      }
    }

    // Same URL resolution rule as getActiveElementUrl: route through the API
    // gateway in prod, direct to :9222 in local dev.
    private getEmulateDeviceUrl(): string {
      const params = new URLSearchParams(window.location.search)
      const sessionId = params.get('session_id')
      const token = params.get('token')
      if (sessionId && token) {
        return `${window.location.origin}/api/${sessionId}/${token}/cdp/emulate-device`
      }
      return `http://${window.location.hostname}:9222/cdp/emulate-device`
    }

    private remoteEmulationSynced = false

    @Watch('connected')
    onConnectedForRemoteResize(connected: boolean) {
      if (!connected || this.remoteEmulationSynced) return
      const profile = this.pickDeviceProfile()
      if (!profile) return
      this.remoteEmulationSynced = true

      // Activate the CSS magnify hack: visually zoom into the top-left
      // profile.width x profile.height region of the streamed framebuffer
      // and route taps into that region. Triggers a reactive onResize.
      this.magnifyW = profile.width
      this.magnifyH = profile.height
      this.mobileMagnified = true

      // Pick scale so the mobile page fills the remote framebuffer width.
      // Falls back to 1 if we don't yet know the framebuffer dimensions.
      const fbWidth = this.width || profile.width
      const scale = Math.max(1, fbWidth / profile.width)

      fetch(this.getEmulateDeviceUrl(), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          width: profile.width,
          height: profile.height,
          mobile: profile.mobile,
          scale,
          userAgent: profile.userAgent,
          touch: true,
          maxTouchPoints: profile.maxTouchPoints,
        }),
      })
        .then((res) => {
          if (!res.ok) {
            // Don't unsync — the server isn't going to start working mid-session.
            // Logging only; the rest of the client still works.
            console.warn('[CDP] emulate-device returned', res.status)
          }
        })
        .catch((err) => {
          console.warn('[CDP] emulate-device failed', err)
        })
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

      this.connectCDP()

      // Mobile keyboard detection via visualViewport (ported from
      // keyboard.ts:214-253). When the visual viewport shrinks by >50px the
      // soft keyboard is up; when it grows back the user dismissed it.
      if (this.is_touch_device && window.visualViewport) {
        window.visualViewport.addEventListener('resize', this.handleViewportResize)
      }

      // Android auto-focus poller: if the remote page focuses an input on
      // its own (autofocus, programmatic .focus(), form-flow advance), pop
      // the keyboard automatically. iOS is excluded because Safari requires
      // focus calls inside user gestures.
      this.startAndroidAutoFocusPoller()

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
      if (this.cdpSocket) {
        this.cdpSocket.close()
        this.cdpSocket = null
      }
      this.cancelLongPress()
      if (this.pendingBackspaceTimer !== null) {
        window.clearTimeout(this.pendingBackspaceTimer)
        this.pendingBackspaceTimer = null
      }
      if (window.visualViewport) {
        window.visualViewport.removeEventListener('resize', this.handleViewportResize)
      }
      window.removeEventListener('message', this.onPortalMessage)
      this.stopAndroidAutoFocusPoller()
      this.observer.disconnect()
      this.pendingCDPCommands.clear()
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
    // Detect production (behind gateway) vs local dev and return the correct
    // URL for the active-element endpoint.
    // Production path: /<browser_pod_id>/<session_id>/<token>/...
    // Production API:  /api/<session_id>/<token>/cdp/active-element
    // Local:           http://<hostname>:9222/cdp/active-element
    getActiveElementUrl(): string {
      const match = window.location.pathname.match(/^\/(browser[^/]+)\/([^/]+)\/([^/]+)/);
      if (match) {
        const sessionId = match[2];
        const token = match[3];
        return `${window.location.origin}/api/${sessionId}/${token}/cdp/active-element`;
      }
      // Local dev: hit the kernel-images-api directly
      return `http://${window.location.hostname}:9222/cdp/active-element`;
    }

    // Open a page-scoped CDP WebSocket so mobile keystrokes can be injected
    // via Input.insertText / Input.dispatchKeyEvent (which bypass xdotool/X11
    // and handle Unicode trivially). The kernel-images-api filter on :9222
    // already whitelists Input.* methods, so commands go straight through.
    //
    // Connection is page-scoped (ws://.../devtools/page/<id>), which means no
    // Target.attachToTarget dance is needed and commands omit sessionId.
    // On same-tab navigation the targetId is stable; on cross-tab navigation
    // a reconnect would be needed (not handled here — first-iteration scope).
    async connectCDP() {
      try {
        const jsonUrl = `http://${window.location.hostname}:9222/json`
        const res = await fetch(jsonUrl)
        const targets = await res.json()
        const page = Array.isArray(targets)
          ? targets.find((t: any) => t.type === 'page' && !String(t.url || '').startsWith('devtools://'))
          : null
        if (!page || !page.webSocketDebuggerUrl) {
          console.warn('[CDP] no page target available')
          return
        }
        console.log('[CDP] connecting to', page.webSocketDebuggerUrl)
        this.cdpSocket = new WebSocket(page.webSocketDebuggerUrl)
        this.cdpSocket.addEventListener('open', () => {
          console.log('[CDP] socket open — enabling Input domain')
          this.sendCDPCommand('Input.enable', {})
          // Note: we intentionally do NOT enable Runtime here. Runtime.evaluate
          // isn't in the WSS proxy allowlist (proxy.go:564) — calls would be
          // rejected with "Command not allowed". Any place that needs eval
          // goes via a server-side endpoint (/cdp/active-element, /cdp/
          // set-select-value) which uses the unfiltered upstream socket.
        })
        this.cdpSocket.addEventListener('message', (e) => {
          // Dispatch responses to any awaiting sendCDPCommandWithResult call.
          try {
            const msg = JSON.parse(e.data as string)
            if (msg && typeof msg.id === 'number') {
              const pending = this.pendingCDPCommands.get(msg.id)
              if (pending) {
                this.pendingCDPCommands.delete(msg.id)
                if (msg.error) pending.reject(msg.error)
                else pending.resolve(msg.result)
              }
            }
          } catch { /* ignore parse errors */ }
        })
        this.cdpSocket.addEventListener('error', (e) => {
          console.warn('[CDP] socket error', e)
        })
        this.cdpSocket.addEventListener('close', () => {
          console.log('[CDP] socket closed')
          this.cdpSocket = null
        })
      } catch (err) {
        console.warn('[CDP] connect failed', err)
      }
    }

    private sendCDPCommand(method: string, params: unknown = {}): number | null {
      if (!this.cdpSocket || this.cdpSocket.readyState !== WebSocket.OPEN) return null
      const id = this.cdpCommandId++
      this.cdpSocket.send(JSON.stringify({ id, method, params }))
      return id
    }

    // Fire a CDP command and await its response. Used for Runtime.evaluate
    // calls where we need the returned value (e.g., applying a select value).
    private sendCDPCommandWithResult(method: string, params: unknown = {}): Promise<any> {
      if (!this.cdpSocket || this.cdpSocket.readyState !== WebSocket.OPEN) {
        return Promise.reject(new Error('CDP socket not open'))
      }
      const id = this.cdpCommandId++
      return new Promise((resolve, reject) => {
        this.pendingCDPCommands.set(id, { resolve, reject })
        this.cdpSocket!.send(JSON.stringify({ id, method, params }))
        // Safety timeout so we don't leak entries on dropped responses.
        window.setTimeout(() => {
          if (this.pendingCDPCommands.has(id)) {
            this.pendingCDPCommands.delete(id)
            reject(new Error(`CDP command ${method} timed out`))
          }
        }, 5000)
      })
    }

    // Insert plain text via CDP. Goes directly into chromium's input pipeline
    // — no xdotool, no keysym translation, full Unicode support.
    private sendCDPText(text: string) {
      if (!text) return
      this.sendCDPCommand('Input.insertText', { text })
    }

    // Dispatch a synthetic key event (Backspace/Enter/Tab/etc). Three-event
    // sequence (keyDown + char + keyUp) matches what a real keyboard produces,
    // so chromium handlers that listen for any phase still fire.
    private sendCDPSpecialKey(name: 'Backspace' | 'Enter' | 'Tab') {
      const map: Record<string, { key: string; code: string; keyCode: number; text: string }> = {
        Enter:     { key: 'Enter',     code: 'Enter',     keyCode: 13, text: '\r' },
        Backspace: { key: 'Backspace', code: 'Backspace', keyCode: 8,  text: '\b' },
        Tab:       { key: 'Tab',       code: 'Tab',       keyCode: 9,  text: '\t' },
      }
      const m = map[name]
      if (!m) return
      this.sendCDPCommand('Input.dispatchKeyEvent', {
        type: 'keyDown', key: m.key, code: m.code,
        windowsVirtualKeyCode: m.keyCode, nativeVirtualKeyCode: m.keyCode, text: m.text,
      })
      this.sendCDPCommand('Input.dispatchKeyEvent', {
        type: 'char', key: m.key, code: m.code,
        windowsVirtualKeyCode: m.keyCode, nativeVirtualKeyCode: m.keyCode, text: m.text,
      })
      this.sendCDPCommand('Input.dispatchKeyEvent', {
        type: 'keyUp', key: m.key, code: m.code,
        windowsVirtualKeyCode: m.keyCode, nativeVirtualKeyCode: m.keyCode,
      })
    }

    // Async version of checkElementHasFocusSync. Used by the Android
    // auto-focus poller — async because polling doesn't need to stay in
    // the iOS gesture chain. Returns null on transport error.
    async checkElementHasFocus(): Promise<{
      isInput: boolean
      tag: string
      type?: string
      isEditable?: boolean
      readonly?: boolean
      disabled?: boolean
      focusKey?: string
      elementTop?: number
      elementHeight?: number
      selectInfo?: {
        multiple: boolean
        rect: { x: number; y: number; width: number; height: number }
        options: Array<{ value: string; text: string; selected: boolean; disabled: boolean; groupLabel?: string }>
      }
    } | null> {
      try {
        const res = await fetch(this.getActiveElementUrl())
        if (!res.ok) return null
        const data = await res.json()
        return {
          isInput: !!data.isInput,
          tag: data.tag || 'unknown',
          type: data.type,
          isEditable: data.isEditable,
          readonly: !!data.readonly,
          disabled: !!data.disabled,
          focusKey: data.focusKey,
          elementTop: data.elementTop,
          elementHeight: data.elementHeight,
          selectInfo: data.selectInfo,
        }
      } catch {
        return null
      }
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
      return `http://${window.location.hostname}:9222/cdp/set-select-value`
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

    // Android auto-focus poller (ported from keyboard.ts/useMobileKeyboard.ts:
    // 249-319). Every 800 ms while the keyboard is closed, peek at the remote
    // active element. If it's a focusable text input the user didn't just
    // dismiss, pop the keyboard automatically — matches the UX of typing on
    // the remote site directly. iOS is excluded because Safari only honors
    // .focus() inside user gestures, not setInterval ticks.
    startAndroidAutoFocusPoller() {
      if (this.autoFocusPoll !== null) return
      if (!this.isAndroid || !this.is_touch_device) return

      let consecutiveErrors = 0
      const tick = async () => {
        if (this.keyboardActive) return
        if (this.keyboardOpening) return
        if (this.keyboardJustDismissed) return
        if (document.hidden) return

        const info = await this.checkElementHasFocus()
        if (info === null) {
          if (++consecutiveErrors >= 5) {
            console.warn('[auto-focus poller] too many errors, stopping')
            this.stopAndroidAutoFocusPoller()
          }
          return
        }
        consecutiveErrors = 0
        if (!info.isInput) {
          // Focus moved off any input — clear the suppression so a future
          // return to a previously-dismissed element re-triggers.
          this.suppressedFocusKey = null
          return
        }
        if (info.readonly || info.disabled) return
        // Skip if user explicitly dismissed the keyboard on this exact element.
        if (info.focusKey && this.suppressedFocusKey === info.focusKey) return
        this.suppressedFocusKey = null
        this.focusProxyInputForIOS()
      }

      this.autoFocusPoll = window.setInterval(tick, 800)
      // Fire once immediately so an existing autofocus is caught without
      // waiting a full interval.
      tick()
    }

    stopAndroidAutoFocusPoller() {
      if (this.autoFocusPoll !== null) {
        window.clearInterval(this.autoFocusPoll)
        this.autoFocusPoll = null
      }
    }

    /**
     * Synchronous check using XMLHttpRequest.
     * Blocks the main thread until the response arrives, which is acceptable
     * here because: (a) it's hitting localhost (~1-5ms), and (b) we MUST know
     * the result before deciding to focus (Safari keyboard limitation).
     */
    checkElementHasFocusSync(): { isInput: boolean, tag: string, rawOuterHTML?: string, type?: string, isEditable?: boolean } {
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
      // In magnify mode the iframe only shows the top-left magnifyW x magnifyH
      // region of the framebuffer; tap coords must map onto that region, not
      // onto the full 1920x1080 framebuffer.
      const w = this.mobileMagnified && this.magnifyW > 0
        ? this.magnifyW
        : this.$accessor.video.resolution.w
      const h = this.mobileMagnified && this.magnifyH > 0
        ? this.magnifyH
        : this.$accessor.video.resolution.h
      const rect = this._overlay.getBoundingClientRect()

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

          const moveDx = touch.clientX - this.touchLastX
          const moveDy = touch.clientY - this.touchLastY
          this.touchLastX = touch.clientX
          this.touchLastY = touch.clientY

          const now = Date.now()
          if (now - this.touchLastWheelEmit < 60) return
          this.touchLastWheelEmit = now

          // Native feel: swiping the finger UP scrolls content UP (= positive
          // wheel deltaY). The remote interprets +y as scroll-down lines.
          let wheelX = -Math.round(moveDx / 6)
          let wheelY = -Math.round(moveDy / 6)
          wheelX = Math.min(Math.max(wheelX, -this.scroll), this.scroll)
          wheelY = Math.min(Math.max(wheelY, -this.scroll), this.scroll)

          if (wheelX || wheelY) {
            this.sendMousePosFromTouch(touch)
            this.$client.sendData('wheel', { x: wheelX, y: wheelY })
          }
          break
        }

        case 'touchend':
        case 'touchcancel': {
          this.cancelLongPress()
          const mode = this.touchMode
          const duration = Date.now() - this.touchStartTime
          this.touchMode = 'idle'

          if (e.type === 'touchcancel') return

          if (mode === 'pending' && duration < 300) {
            // Tap: synthesize click at touch coords, then run the existing
            // soft-keyboard focus check (synchronous XHR → CDP active element).
            this.sendMousePosFromTouch(touch)
            this.$client.sendData('mousedown', { key: 1 })
            this.$client.sendData('mouseup', { key: 1 })

            if (this.is_touch_device && !this.keyboardJustDismissed) {
              const elementResult = this.checkElementHasFocusSync()
              if (elementResult.isInput) {
                // Synchronous focus inside touch gesture so iOS pops the
                // keyboard. Goes through the iOS temp-input bridge when
                // standalone, direct focus otherwise.
                this.focusProxyInputForIOS()
              } else if (this.keyboardActive) {
                // Tapped a non-input while keyboard is up → dismiss.
                this.allowBlur = true
                if (this._proxyInput) this._proxyInput.blur()
                window.setTimeout(() => { this.allowBlur = false }, 100)
              }
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
      const w = this.effectiveW
      const h = this.effectiveH
      const rect = this._overlay.getBoundingClientRect()
      this.$client.sendData('mousemove', {
        x: Math.round((w / rect.width) * (t.clientX - rect.left)),
        y: Math.round((h / rect.height) * (t.clientY - rect.top)),
      })
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
          for (let i = 0; i < deletedCount; i++) this.sendCDPSpecialKey('Backspace')
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
              for (let i = 0; i < this.lastSentValue.length; i++) this.sendCDPSpecialKey('Backspace')
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
            for (let i = 0; i < this.lastSentValue.length; i++) this.sendCDPSpecialKey('Backspace')
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
      // If the user dismissed the keyboard via the system back button (not
      // via our toggleMobileKeyboard which sets allowBlur=true first), record
      // a 100ms grace window so a touchend that immediately follows doesn't
      // re-pop the keyboard.
      if (this.keyboardActive && !this.allowBlur && !document.hidden) {
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
      if (this.keyboardOpening) return

      const shrunk = (windowHeight - viewportHeight) > 50
      if (shrunk) {
        this.lastViewportShrink = true
        this.keyboardActive = true
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

      // iOS sometimes fires a transient resize during the keyboard show
      // animation that looks like "viewport restored" — guard via active
      // element identity. If our proxy is still focused, the keyboard
      // is actually still up.
      const proxyStillFocused = document.activeElement === this._proxyInput
      if (Math.abs(viewportHeight - windowHeight) < 50 && this.keyboardActive && !proxyStillFocused) {
        this.keyboardActive = false
        this.iframeOffset = 0
        window.scrollTo(0, 0)
        this.keyboardJustDismissed = true
        window.setTimeout(() => { this.keyboardJustDismissed = false }, 100)
        if (this._proxyInput) this._proxyInput.blur()
        // Record which remote element the user was on so the auto-focus
        // poller (Android only) doesn't re-pop the keyboard on the same
        // field. iOS doesn't run the poller, so skip the CDP roundtrip there.
        if (this.isAndroid) {
          this.checkElementHasFocus().then(info => {
            if (info?.focusKey) this.suppressedFocusKey = info.focusKey
          }).catch(() => { /* best-effort */ })
        }
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

      // In magnify mode, target the *emulated* viewport's aspect ratio (e.g.
      // 9:19.5 phone) so the visible region of the framebuffer fills the
      // container with no letterbox. Otherwise stick to the framebuffer's
      // native aspect (16:9 desktop).
      const aspectW = this.mobileMagnified && this.magnifyW > 0 ? this.magnifyW : this.horizontal
      const aspectH = this.mobileMagnified && this.magnifyH > 0 ? this.magnifyH : this.vertical

      const aspectPreservingMaxWidth = (aspectW / aspectH) * offsetHeight
      const ceiling = this.mobileMagnified && this.magnifyW > 0 ? offsetWidth : this.width
      this._container.style.maxWidth = `${
        !this.fullscreen ? Math.min(ceiling, aspectPreservingMaxWidth) : aspectPreservingMaxWidth
      }px`
      this._aspect.style.paddingBottom = `${(aspectH / aspectW) * 100}%`

      // Publish the resolved container dims for overlayInfo / magnifyStyle.
      // Read after the aspect/maxWidth assignments so we see the final layout.
      this.containerW = this._container.offsetWidth
      this.containerH = this._container.offsetHeight
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

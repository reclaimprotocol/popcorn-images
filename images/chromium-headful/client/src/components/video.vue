<template>
  <div ref="component" class="video">
    <div ref="player" class="player">
      <div ref="container" class="player-container">
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
        />
        <!-- Mobile keyboard input: separate from overlay to avoid .prevent conflicts.
             Focused from the keyboard toggle button (clean click gesture).
             Text captured via input events and sent through CDP. -->
        <input
          v-if="is_touch_device"
          ref="mobileInput"
          type="text"
          inputmode="text"
          autocomplete="off"
          autocorrect="off"
          autocapitalize="off"
          spellcheck="false"
          class="mobile-keyboard-input"
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

      /* On mobile: fill screen, no centering */
      @media (pointer: coarse) {
        justify-content: flex-start;
        align-items: flex-start;
        width: 100vw !important;
        height: 100vh !important;
      }

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

        /* Mobile viewport: X11 display is resized to phone dimensions.
           Video stream matches the screen — default neko CSS handles it. */

        .mobile-keyboard-input {
          position: absolute;
          top: 50%;
          left: 50%;
          width: 1px;
          height: 1px;
          opacity: 0.01;
          font-size: 16px; /* prevents iOS zoom */
          border: 0;
          outline: 0;
          z-index: 200;
        }

        .player-aspect {
          display: block;
          padding-bottom: 56.25%;
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
    @Ref('mobileInput') readonly _mobileInput!: HTMLInputElement
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

    // CDP state
    private cdpSocket: WebSocket | null = null
    private pendingCDPCommands: Map<number, { resolve: Function, reject: Function }> = new Map()
    private cdpCommandId = 1
    private cdpSessionId: string | null = null

    // Viewport & magnify state (same as keyboard.ts)
    private readonly PHYSICAL_WIDTH = 1920
    private readonly PHYSICAL_HEIGHT = 1080
    // Desktop: magnified (native 1920x1080, neko handles everything)
    // Mobile: fit-to-screen (CDP emulation + stealth patches for bot detection)
    isMagnified = !(('ontouchstart' in window || navigator.maxTouchPoints > 0) && window.matchMedia('(pointer: coarse)').matches)
    private emulatedWidth = 0
    private emulatedHeight = 0

    // Touch scroll state (for CDP touch events)
    private touchStart: { x: number; y: number; clientX: number; clientY: number; time: number } | null = null
    private isScrolling = false
    private touchEventsSent = false

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

    // Mobile viewport emulation: active only on touch devices when not magnified.
    // Desktop never enters this mode — neko handles everything at native 1920x1080.
    get mobileViewportActive(): boolean {
      return this.is_touch_device && !this.isMagnified
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

    private isVideoSyncing = false;

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

      // Re-apply layout on resize
      window.addEventListener('resize', () => {
        this.onResize()
      })

      this.connectCDP()

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
        this.isVideoSyncing = true;
        this.$accessor.video.play()
        this.$nextTick(() => { this.isVideoSyncing = false; })
      })

      this._video.addEventListener('pause', () => {
        this.isVideoSyncing = true;
        this.$accessor.video.pause()
        this.$nextTick(() => { this.isVideoSyncing = false; })
      });

      /* Guacamole Keyboard — desktop uses this always (neko → X11).
       * Mobile in emulated mode uses CDP keyboard handlers below instead. */
      this.keyboard.onkeydown = (key: number) => {
        if (!this.hosting || this.locked) return true
        if (this.mobileViewportActive) return true  // mobile CDP takes over
        this.$client.sendData('keydown', { key: this.keyMap(key) })
        return false
      }
      this.keyboard.onkeyup = (key: number) => {
        if (!this.hosting || this.locked) return
        if (this.mobileViewportActive) return  // mobile CDP takes over
        this.$client.sendData('keyup', { key: this.keyMap(key) })
      }
      this.keyboard.listenTo(this._overlay)

      /* Emulated mode keyboard: native keydown/keyup + clipboard handlers.
       * Bypasses Guacamole to preserve modifier state (Ctrl+C/V), handle
       * clipboard, and send via CDP like keyboard.ts. */
      this._overlay.addEventListener('keydown', (e: KeyboardEvent) => {
        if (!this.mobileViewportActive || !this.hosting || this.locked) return

        // Clipboard shortcuts
        if (e.metaKey || e.ctrlKey) {
          const key = e.key.toLowerCase()

          // Paste (Cmd/Ctrl+V): read clipboard and send via CDP
          if (key === 'v') {
            e.preventDefault()
            e.stopPropagation()
            navigator.clipboard.readText().then(text => {
              if (text) this.sendCDPToPage('Input.insertText', { text })
            }).catch(() => {
              // Clipboard API failed, try native paste as fallback
              document.execCommand('paste')
            })
            return
          }

          // Copy (Cmd/Ctrl+C): send to remote browser, neko clipboard sync picks it up
          if (key === 'c') {
            e.preventDefault()
            e.stopPropagation()
            this.sendCDPKeyCombo('c', 'KeyC', 67)
            return
          }

          // Cut (Cmd/Ctrl+X)
          if (key === 'x') {
            e.preventDefault()
            e.stopPropagation()
            this.sendCDPKeyCombo('x', 'KeyX', 88)
            return
          }

          // Select All (Cmd/Ctrl+A)
          if (key === 'a') {
            e.preventDefault()
            e.stopPropagation()
            this.sendCDPKeyCombo('a', 'KeyA', 65)
            return
          }

          // Let other Cmd shortcuts pass (Cmd+T, Cmd+W, etc.)
          return
        }

        e.preventDefault()
        e.stopPropagation()

        // Printable character
        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
          if (e.repeat) return
          this.sendCDPToPage('Input.insertText', { text: e.key })
          return
        }

        // Special keys
        const modifiers = (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0)
        this.sendCDPToPage('Input.dispatchKeyEvent', {
          type: 'keyDown', key: e.key, code: e.code,
          windowsVirtualKeyCode: e.keyCode, nativeVirtualKeyCode: e.keyCode,
          modifiers
        })
      }, true)

      this._overlay.addEventListener('keyup', (e: KeyboardEvent) => {
        if (!this.mobileViewportActive || !this.hosting || this.locked) return
        if (e.metaKey || e.ctrlKey) return

        e.preventDefault()
        e.stopPropagation()

        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) return // printable chars handled in keydown

        const modifiers = (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0)
        this.sendCDPToPage('Input.dispatchKeyEvent', {
          type: 'keyUp', key: e.key, code: e.code,
          windowsVirtualKeyCode: e.keyCode, nativeVirtualKeyCode: e.keyCode,
          modifiers
        })
      }, true)

      // Paste event handler: reads clipboard and sends text via CDP
      this._overlay.addEventListener('paste', (e: ClipboardEvent) => {
        if (!this.mobileViewportActive || !this.hosting) return
        e.preventDefault()
        e.stopPropagation()
        const text = e.clipboardData?.getData('text/plain')
        if (text) {
          this.sendCDPToPage('Input.insertText', { text })
        }
      }, true)

      /* Mobile keyboard input via CDP (same approach as keyboard.ts).
       * Sends text via Input.insertText and special keys via Input.dispatchKeyEvent
       * directly to the remote browser's focused element. */
      if (this.is_touch_device && this._mobileInput) {
        const input = this._mobileInput
        const isAndroid = /android/i.test(navigator.userAgent)
        let lastValue = ''

        // Send text via CDP Input.insertText (works for all Unicode)
        const sendChar = (char: string) => {
          this.sendCDPToPage('Input.insertText', { text: char })
        }

        // Send special key via CDP Input.dispatchKeyEvent
        const sendSpecialKey = (key: string, code: string, keyCode: number) => {
          this.sendCDPToPage('Input.dispatchKeyEvent', {
            type: 'keyDown', key, code,
            windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode
          })
          this.sendCDPToPage('Input.dispatchKeyEvent', {
            type: 'keyUp', key, code,
            windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode
          })
        }

        // Deferred backspace timer for IME keyboards that send key='Unidentified'
        let pendingBackspaceTimer: ReturnType<typeof setTimeout> | null = null

        input.addEventListener('beforeinput', (e: Event) => {
          const ev = e as InputEvent
          if (!this.hosting || this.locked) return

          if (!isAndroid) {
            // iOS: handle in beforeinput (more reliable than input event)
            if (ev.inputType === 'insertText' && ev.data) {
              ev.preventDefault()
              for (const char of ev.data) sendChar(char)
              input.value = ''
              lastValue = ''
            } else if (ev.inputType === 'deleteContentBackward') {
              ev.preventDefault()
              sendSpecialKey('Backspace', 'Backspace', 8)
              input.value = ''
              lastValue = ''
            } else if (ev.inputType === 'insertLineBreak') {
              ev.preventDefault()
              sendSpecialKey('Enter', 'Enter', 13)
              input.value = ''
              lastValue = ''
            }
          } else {
            // Android: handle deletions in beforeinput
            if (ev.inputType === 'deleteContentBackward' || ev.inputType === 'deleteByCut' ||
                ev.inputType === 'deleteContent' || ev.inputType === 'deleteContentForward') {
              if (pendingBackspaceTimer) { clearTimeout(pendingBackspaceTimer); pendingBackspaceTimer = null }
              if (input.value === '') {
                ev.preventDefault()
                sendSpecialKey('Backspace', 'Backspace', 8)
                return
              }
              // Let through to input handler for value-based detection
              return
            }
            if (pendingBackspaceTimer) { clearTimeout(pendingBackspaceTimer); pendingBackspaceTimer = null }
            if (ev.inputType === 'insertLineBreak') {
              ev.preventDefault()
              sendSpecialKey('Enter', 'Enter', 13)
              input.value = ''
              lastValue = ''
            }
          }
        })

        input.addEventListener('input', (e: Event) => {
          if (!this.hosting || this.locked) { input.value = ''; lastValue = ''; return }

          if (isAndroid) {
            const inputEvent = e as InputEvent
            const cur = input.value

            // Cancel deferred backspace
            if (pendingBackspaceTimer) { clearTimeout(pendingBackspaceTimer); pendingBackspaceTimer = null }

            // Check inputType for deletions
            if (inputEvent.inputType === 'deleteContentBackward' || inputEvent.inputType === 'deleteByCut' ||
                inputEvent.inputType === 'deleteContent' || inputEvent.inputType === 'deleteContentForward') {
              sendSpecialKey('Backspace', 'Backspace', 8)
              lastValue = cur
              return
            }

            // Detect new characters by comparing values
            if (cur.length > lastValue.length) {
              const newChars = cur.startsWith(lastValue) ? cur.slice(lastValue.length) : cur
              for (const char of newChars) sendChar(char)
            } else if (cur.length < lastValue.length) {
              const deleted = lastValue.length - cur.length
              for (let i = 0; i < deleted; i++) sendSpecialKey('Backspace', 'Backspace', 8)
            }
            lastValue = cur
            if (cur.length > 50) { input.value = cur.slice(-5); lastValue = input.value }
          }
        })

        input.addEventListener('keydown', (e: KeyboardEvent) => {
          if (!this.hosting || this.locked) return
          if (e.key === 'Enter') {
            e.preventDefault()
            sendSpecialKey('Enter', 'Enter', 13)
            input.value = ''; lastValue = ''
          } else if (e.key === 'Backspace' && input.value === '') {
            e.preventDefault()
            sendSpecialKey('Backspace', 'Backspace', 8)
          } else if (e.key === 'Tab') {
            e.preventDefault()
            sendSpecialKey('Tab', 'Tab', 9)
          } else if (e.key === 'Unidentified' && input.value === '') {
            // IME keyboards: schedule deferred backspace, cancelled if input event fires
            if (pendingBackspaceTimer) clearTimeout(pendingBackspaceTimer)
            pendingBackspaceTimer = setTimeout(() => {
              pendingBackspaceTimer = null
              sendSpecialKey('Backspace', 'Backspace', 8)
            }, 50)
          }
        })
      }
    }

    beforeDestroy() {
      if (this.cdpSocket) {
        this.cdpSocket.close()
        this.cdpSocket = null
      }
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

    // CDP input via HTTP POST to /cdp/input — no WebSocket needed.
    // The server's FocusTracker shares its CDP connection and session ID.
    // This avoids Chrome's concurrent DevTools connection limit.
    async connectCDP() {
      console.log(`[CDP] Using HTTP input endpoint: ${this.getCDPInputUrl()}`);
      // No viewport emulation — keep native 1920x1080 on all devices.
      // Mobile users see desktop layout via neko stream. This avoids
      // Cloudflare detection (small resolutions on Linux Chrome = suspicious).
    }

    getCDPInputUrl(): string {
      const match = window.location.pathname.match(/^\/(browser[^/]+)\/([^/]+)\/([^/]+)/);
      if (match) {
        return `${window.location.origin}/api/${match[2]}/${match[3]}/cdp/input`;
      }
      return `http://${window.location.hostname}:9222/cdp/input`;
    }

    // Send CDP command via HTTP POST (disabled for bot-detection testing)
    sendCDPToPage(method: string, params: any = {}) {
      return; // DISABLED: CDP calls trigger Cloudflare detection
      const url = this.getCDPInputUrl();
      fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method, params }),
      }).then(res => {
        if (!res.ok) {
          res.text().then(t => console.error(`[CDP] ${method} failed: ${res.status} ${t}`));
        }
      }).catch(err => console.warn(`[CDP] ${method} fetch error:`, err));
    }

    // ─── Mobile viewport: real X11 display resize (no CDP, no bot detection) ───
    toggleMagnify() {
      this.isMagnified = !this.isMagnified;
    }

    // ─── Keysym → CDP key info (for emulated mode keyboard) ───

    private SPECIAL_KEYS: Record<number, { key: string; code: string; keyCode: number }> = {
      0xFF08: { key: 'Backspace', code: 'Backspace', keyCode: 8 },
      0xFF09: { key: 'Tab', code: 'Tab', keyCode: 9 },
      0xFF0D: { key: 'Enter', code: 'Enter', keyCode: 13 },
      0xFF1B: { key: 'Escape', code: 'Escape', keyCode: 27 },
      0xFF50: { key: 'Home', code: 'Home', keyCode: 36 },
      0xFF51: { key: 'ArrowLeft', code: 'ArrowLeft', keyCode: 37 },
      0xFF52: { key: 'ArrowUp', code: 'ArrowUp', keyCode: 38 },
      0xFF53: { key: 'ArrowRight', code: 'ArrowRight', keyCode: 39 },
      0xFF54: { key: 'ArrowDown', code: 'ArrowDown', keyCode: 40 },
      0xFF55: { key: 'PageUp', code: 'PageUp', keyCode: 33 },
      0xFF56: { key: 'PageDown', code: 'PageDown', keyCode: 34 },
      0xFF57: { key: 'End', code: 'End', keyCode: 35 },
      0xFFFF: { key: 'Delete', code: 'Delete', keyCode: 46 },
      0xFFE1: { key: 'Shift', code: 'ShiftLeft', keyCode: 16 },
      0xFFE2: { key: 'Shift', code: 'ShiftRight', keyCode: 16 },
      0xFFE3: { key: 'Control', code: 'ControlLeft', keyCode: 17 },
      0xFFE4: { key: 'Control', code: 'ControlRight', keyCode: 17 },
      0xFFE9: { key: 'Alt', code: 'AltLeft', keyCode: 18 },
      0xFFEA: { key: 'Alt', code: 'AltRight', keyCode: 18 },
      0xFFEB: { key: 'Meta', code: 'MetaLeft', keyCode: 91 },
      0xFFEC: { key: 'Meta', code: 'MetaRight', keyCode: 91 },
      0x0020: { key: ' ', code: 'Space', keyCode: 32 },
    }

    keysymToKeyInfo(keysym: number): { key: string; code: string; keyCode: number; text?: string } {
      // Check special keys
      const special = this.SPECIAL_KEYS[keysym]
      if (special) return special

      // Printable character: convert keysym to Unicode
      let char: string
      if (keysym >= 0x01000000) {
        // Unicode keysym: 0x01000000 | codepoint
        char = String.fromCodePoint(keysym & 0x00FFFFFF)
      } else if (keysym >= 0x20 && keysym <= 0x7E) {
        // ASCII printable
        char = String.fromCharCode(keysym)
      } else {
        // Unknown — send as key event
        char = String.fromCharCode(keysym)
      }

      return { key: char, code: '', keyCode: char.charCodeAt(0), text: char }
    }

    // ─── Coordinate translation (screen → emulated, same as keyboard.ts) ───

    getEmulatedCoords(clientX: number, clientY: number): { x: number; y: number } {
      const rect = this._overlay.getBoundingClientRect();
      const targetW = this.emulatedWidth || window.innerWidth;
      const targetH = this.emulatedHeight || window.innerHeight;

      const relX = clientX - rect.left;
      const relY = clientY - rect.top;

      const x = Math.round((relX / rect.width) * targetW);
      const y = Math.round((relY / rect.height) * targetH);

      return {
        x: Math.max(0, Math.min(targetW - 1, x)),
        y: Math.max(0, Math.min(targetH - 1, y))
      };
    }

    // ─── CDP clipboard (copy from remote browser to local clipboard) ───

    // Send Ctrl+C / Ctrl+X to remote browser via CDP.
    // The remote browser copies to its clipboard, neko's clipboard sync picks it up.
    sendCDPKeyCombo(key: string, code: string, keyCode: number) {
      // keyDown with Ctrl modifier
      this.sendCDPToPage('Input.dispatchKeyEvent', {
        type: 'keyDown', key, code,
        windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode,
        modifiers: 2 // Ctrl
      })
      this.sendCDPToPage('Input.dispatchKeyEvent', {
        type: 'keyUp', key, code,
        windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode,
        modifiers: 2
      })
    }

    // ─── CDP touch scroll (same as keyboard.ts handleTouchStart/Move/EndKernel) ───

    sendCDPClick(x: number, y: number) {
      this.sendCDPToPage('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
      this.sendCDPToPage('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
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
      const w = this.$accessor.video.resolution.w
      const h = this.$accessor.video.resolution.h
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

      if (e.deltaMode !== 0) {
        x *= WHEEL_LINE_HEIGHT
        y *= WHEEL_LINE_HEIGHT
      }

      if (this.scroll_invert) {
        x = x * -1
        y = y * -1
      }

      x = Math.min(Math.max(x, -this.scroll), this.scroll)
      y = Math.min(Math.max(y, -this.scroll), this.scroll)

      if (this.mobileViewportActive) {
        // Mobile: send via CDP with emulated coordinates
        const { x: emuX, y: emuY } = this.getEmulatedCoords(e.clientX, e.clientY)
        this.sendCDPToPage('Input.dispatchMouseEvent', {
          type: 'mouseWheel', x: emuX, y: emuY, deltaX: x, deltaY: y
        })
      } else {
        // Desktop: send via neko
        this.sendMousePos(e)
        if (!this.wheelThrottle) {
          this.wheelThrottle = true
          this.$client.sendData('wheel', { x, y })
          window.setTimeout(() => { this.wheelThrottle = false }, 100)
        }
      }
    }

    // ─── Touch handler ───
    // All touch input goes through neko (simulated mouse events at 1920x1080).
    // On mobile viewport, the CSS-scaled overlay maps touch coords to 1920x1080
    // automatically via getEmulatedCoords. No CDP — no bot detection.
    onTouchHandler(e: TouchEvent) {
      const first = e.changedTouches[0]
      if (!first) return

      // Convert touch to neko mouse coordinates (works for both scaled and native)
      const rect = this._overlay.getBoundingClientRect()
      const w = this.$accessor.video.resolution.w || this.PHYSICAL_WIDTH
      const h = this.$accessor.video.resolution.h || this.PHYSICAL_HEIGHT
      const x = Math.round((w / rect.width) * (first.clientX - rect.left))
      const y = Math.round((h / rect.height) * (first.clientY - rect.top))

      switch (e.type) {
        case 'touchstart':
          this.touchStart = { x, y, clientX: first.clientX, clientY: first.clientY, time: Date.now() }
          this.isScrolling = false
          this.$client.sendData('mousemove', { x, y })
          this.$client.sendData('mousedown', { key: 1 })
          break

        case 'touchmove':
          if (!this.touchStart) return
          const dx = Math.abs(this.touchStart.clientX - first.clientX)
          const dy = Math.abs(this.touchStart.clientY - first.clientY)
          if (dx > 10 || dy > 10) this.isScrolling = true
          this.$client.sendData('mousemove', { x, y })
          break

        case 'touchend':
          this.$client.sendData('mouseup', { key: 1 })

          if (this.touchStart && !this.isScrolling) {
            const duration = Date.now() - this.touchStart.time
            if (duration < 300) {
              // Tap — check if input field for keyboard
              const result = this.checkElementHasFocusSync()
              if (result.isInput && this._mobileInput) {
                this._mobileInput.value = ''
                this._mobileInput.focus()
              } else if (!result.isInput) {
                if (this._mobileInput) this._mobileInput.blur()
                if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
              }
            }
          }

          this.touchStart = null
          this.isScrolling = false
          break
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

    onResize() {
      const { offsetWidth, offsetHeight } = !this.fullscreen ? this._component : document.body
      this._player.style.width = `${offsetWidth}px`
      this._player.style.height = `${offsetHeight}px`
      const aspectPreservingMaxWidth = (this.horizontal / this.vertical) * offsetHeight
      this._container.style.maxWidth = `${!this.fullscreen ? Math.min(this.width, aspectPreservingMaxWidth) : aspectPreservingMaxWidth}px`
      this._aspect.style.paddingBottom = `${(this.vertical / this.horizontal) * 100}%`
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
      // Focus the dedicated mobile input (not the overlay which has .prevent on
      // touch events). This is a click gesture from the keyboard button, so
      // .focus() opens the keyboard on both iOS and Android.
      if (this._mobileInput) {
        this._mobileInput.value = '';
        this._mobileInput.focus();
      } else {
        this._overlay.focus();
      }
    }
  }
</script>

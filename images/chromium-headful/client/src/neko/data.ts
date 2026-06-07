export const OPCODE = {
  MOVE: 0x01,
  SCROLL: 0x02,
  KEY_DOWN: 0x03,
  KEY_UP: 0x04,
  // Touch opcodes. Server side (your neko fork) needs handlers that
  // forward to xf86-input-neko's touch slot API — the driver already
  // supports TOUCH_MAX_SLOTS=10 and registers as XI_TOUCHSCREEN, so it's
  // really just plumbing the wire format through.
  //
  // Wire format (matches existing little-endian pattern):
  //   u8  opcode
  //   u16 length (always 5 for touch events)
  //   u8  slot id (0–9; one per simultaneous finger)
  //   i16 x (CSS pixels, remote viewport coords)
  //   i16 y
  TOUCH_BEGIN:  0x05,  // finger down: register slot, dispatch touchstart
  TOUCH_UPDATE: 0x06,  // finger move: update slot position, dispatch touchmove
  TOUCH_END:    0x07,  // finger up: free slot, dispatch touchend
} as const

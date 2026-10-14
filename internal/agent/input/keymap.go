package input

// keyDef maps a physical key (KeyboardEvent.code) to native key identifiers.
//
//	win: PC/AT set-1 scan code; 0xE0xx marks extended keys. If zero, winVK
//	     is sent as a virtual key instead (media keys, NumLock, Pause).
//	mac: macOS virtual key code (kVK_*); macNone if the key has no equivalent.
//	x11: X keysym name for xdotool.
//
// Using physical keys rather than characters means the remote machine's
// keyboard layout decides which character is typed, exactly as if the
// keyboard were plugged into it.
type keyDef struct {
	win   uint16
	winVK uint16
	mac   uint16
	x11   string
}

const macNone = 0xFFFF

var keymap = map[string]keyDef{
	"Escape":    {win: 0x01, mac: 0x35, x11: "Escape"},
	"Digit1":    {win: 0x02, mac: 0x12, x11: "1"},
	"Digit2":    {win: 0x03, mac: 0x13, x11: "2"},
	"Digit3":    {win: 0x04, mac: 0x14, x11: "3"},
	"Digit4":    {win: 0x05, mac: 0x15, x11: "4"},
	"Digit5":    {win: 0x06, mac: 0x17, x11: "5"},
	"Digit6":    {win: 0x07, mac: 0x16, x11: "6"},
	"Digit7":    {win: 0x08, mac: 0x1A, x11: "7"},
	"Digit8":    {win: 0x09, mac: 0x1C, x11: "8"},
	"Digit9":    {win: 0x0A, mac: 0x19, x11: "9"},
	"Digit0":    {win: 0x0B, mac: 0x1D, x11: "0"},
	"Minus":     {win: 0x0C, mac: 0x1B, x11: "minus"},
	"Equal":     {win: 0x0D, mac: 0x18, x11: "equal"},
	"Backspace": {win: 0x0E, mac: 0x33, x11: "BackSpace"},
	"Tab":       {win: 0x0F, mac: 0x30, x11: "Tab"},

	"KeyQ":         {win: 0x10, mac: 0x0C, x11: "q"},
	"KeyW":         {win: 0x11, mac: 0x0D, x11: "w"},
	"KeyE":         {win: 0x12, mac: 0x0E, x11: "e"},
	"KeyR":         {win: 0x13, mac: 0x0F, x11: "r"},
	"KeyT":         {win: 0x14, mac: 0x11, x11: "t"},
	"KeyY":         {win: 0x15, mac: 0x10, x11: "y"},
	"KeyU":         {win: 0x16, mac: 0x20, x11: "u"},
	"KeyI":         {win: 0x17, mac: 0x22, x11: "i"},
	"KeyO":         {win: 0x18, mac: 0x1F, x11: "o"},
	"KeyP":         {win: 0x19, mac: 0x23, x11: "p"},
	"BracketLeft":  {win: 0x1A, mac: 0x21, x11: "bracketleft"},
	"BracketRight": {win: 0x1B, mac: 0x1E, x11: "bracketright"},
	"Enter":        {win: 0x1C, mac: 0x24, x11: "Return"},
	"ControlLeft":  {win: 0x1D, mac: 0x3B, x11: "Control_L"},

	"KeyA":      {win: 0x1E, mac: 0x00, x11: "a"},
	"KeyS":      {win: 0x1F, mac: 0x01, x11: "s"},
	"KeyD":      {win: 0x20, mac: 0x02, x11: "d"},
	"KeyF":      {win: 0x21, mac: 0x03, x11: "f"},
	"KeyG":      {win: 0x22, mac: 0x05, x11: "g"},
	"KeyH":      {win: 0x23, mac: 0x04, x11: "h"},
	"KeyJ":      {win: 0x24, mac: 0x26, x11: "j"},
	"KeyK":      {win: 0x25, mac: 0x28, x11: "k"},
	"KeyL":      {win: 0x26, mac: 0x25, x11: "l"},
	"Semicolon": {win: 0x27, mac: 0x29, x11: "semicolon"},
	"Quote":     {win: 0x28, mac: 0x27, x11: "apostrophe"},
	"Backquote": {win: 0x29, mac: 0x32, x11: "grave"},
	"ShiftLeft": {win: 0x2A, mac: 0x38, x11: "Shift_L"},
	"Backslash": {win: 0x2B, mac: 0x2A, x11: "backslash"},

	"KeyZ":           {win: 0x2C, mac: 0x06, x11: "z"},
	"KeyX":           {win: 0x2D, mac: 0x07, x11: "x"},
	"KeyC":           {win: 0x2E, mac: 0x08, x11: "c"},
	"KeyV":           {win: 0x2F, mac: 0x09, x11: "v"},
	"KeyB":           {win: 0x30, mac: 0x0B, x11: "b"},
	"KeyN":           {win: 0x31, mac: 0x2D, x11: "n"},
	"KeyM":           {win: 0x32, mac: 0x2E, x11: "m"},
	"Comma":          {win: 0x33, mac: 0x2B, x11: "comma"},
	"Period":         {win: 0x34, mac: 0x2F, x11: "period"},
	"Slash":          {win: 0x35, mac: 0x2C, x11: "slash"},
	"ShiftRight":     {win: 0x36, mac: 0x3C, x11: "Shift_R"},
	"NumpadMultiply": {win: 0x37, mac: 0x43, x11: "KP_Multiply"},
	"AltLeft":        {win: 0x38, mac: 0x3A, x11: "Alt_L"},
	"Space":          {win: 0x39, mac: 0x31, x11: "space"},
	"CapsLock":       {win: 0x3A, mac: 0x39, x11: "Caps_Lock"},

	"F1":  {win: 0x3B, mac: 0x7A, x11: "F1"},
	"F2":  {win: 0x3C, mac: 0x78, x11: "F2"},
	"F3":  {win: 0x3D, mac: 0x63, x11: "F3"},
	"F4":  {win: 0x3E, mac: 0x76, x11: "F4"},
	"F5":  {win: 0x3F, mac: 0x60, x11: "F5"},
	"F6":  {win: 0x40, mac: 0x61, x11: "F6"},
	"F7":  {win: 0x41, mac: 0x62, x11: "F7"},
	"F8":  {win: 0x42, mac: 0x64, x11: "F8"},
	"F9":  {win: 0x43, mac: 0x65, x11: "F9"},
	"F10": {win: 0x44, mac: 0x6D, x11: "F10"},
	"F11": {win: 0x57, mac: 0x67, x11: "F11"},
	"F12": {win: 0x58, mac: 0x6F, x11: "F12"},
	"F13": {win: 0x64, mac: 0x69, x11: "F13"},
	"F14": {win: 0x65, mac: 0x6B, x11: "F14"},
	"F15": {win: 0x66, mac: 0x71, x11: "F15"},

	"NumLock":        {winVK: 0x90, mac: 0x47, x11: "Num_Lock"},
	"ScrollLock":     {win: 0x46, mac: macNone, x11: "Scroll_Lock"},
	"Pause":          {winVK: 0x13, mac: macNone, x11: "Pause"},
	"PrintScreen":    {win: 0xE037, mac: macNone, x11: "Print"},
	"Numpad7":        {win: 0x47, mac: 0x59, x11: "KP_7"},
	"Numpad8":        {win: 0x48, mac: 0x5B, x11: "KP_8"},
	"Numpad9":        {win: 0x49, mac: 0x5C, x11: "KP_9"},
	"NumpadSubtract": {win: 0x4A, mac: 0x4E, x11: "KP_Subtract"},
	"Numpad4":        {win: 0x4B, mac: 0x56, x11: "KP_4"},
	"Numpad5":        {win: 0x4C, mac: 0x57, x11: "KP_5"},
	"Numpad6":        {win: 0x4D, mac: 0x58, x11: "KP_6"},
	"NumpadAdd":      {win: 0x4E, mac: 0x45, x11: "KP_Add"},
	"Numpad1":        {win: 0x4F, mac: 0x53, x11: "KP_1"},
	"Numpad2":        {win: 0x50, mac: 0x54, x11: "KP_2"},
	"Numpad3":        {win: 0x51, mac: 0x55, x11: "KP_3"},
	"Numpad0":        {win: 0x52, mac: 0x52, x11: "KP_0"},
	"NumpadDecimal":  {win: 0x53, mac: 0x41, x11: "KP_Decimal"},
	"NumpadEnter":    {win: 0xE01C, mac: 0x4C, x11: "KP_Enter"},
	"NumpadDivide":   {win: 0xE035, mac: 0x4B, x11: "KP_Divide"},
	"NumpadEqual":    {win: 0x59, mac: 0x51, x11: "KP_Equal"},
	"IntlBackslash":  {win: 0x56, mac: 0x0A, x11: "less"},
	"IntlRo":         {win: 0x73, mac: 0x5E, x11: "backslash"},
	"IntlYen":        {win: 0x7D, mac: 0x5D, x11: "yen"},

	"ControlRight": {win: 0xE01D, mac: 0x3E, x11: "Control_R"},
	"AltRight":     {win: 0xE038, mac: 0x3D, x11: "ISO_Level3_Shift"},
	"MetaLeft":     {win: 0xE05B, mac: 0x37, x11: "Super_L"},
	"MetaRight":    {win: 0xE05C, mac: 0x36, x11: "Super_R"},
	"ContextMenu":  {win: 0xE05D, mac: 0x6E, x11: "Menu"},
	"Home":         {win: 0xE047, mac: 0x73, x11: "Home"},
	"ArrowUp":      {win: 0xE048, mac: 0x7E, x11: "Up"},
	"PageUp":       {win: 0xE049, mac: 0x74, x11: "Prior"},
	"ArrowLeft":    {win: 0xE04B, mac: 0x7B, x11: "Left"},
	"ArrowRight":   {win: 0xE04D, mac: 0x7C, x11: "Right"},
	"End":          {win: 0xE04F, mac: 0x77, x11: "End"},
	"ArrowDown":    {win: 0xE050, mac: 0x7D, x11: "Down"},
	"PageDown":     {win: 0xE051, mac: 0x79, x11: "Next"},
	"Insert":       {win: 0xE052, mac: 0x72, x11: "Insert"},
	"Delete":       {win: 0xE053, mac: 0x75, x11: "Delete"},

	"AudioVolumeMute":    {winVK: 0xAD, mac: 0x4A, x11: "XF86AudioMute"},
	"AudioVolumeDown":    {winVK: 0xAE, mac: 0x49, x11: "XF86AudioLowerVolume"},
	"AudioVolumeUp":      {winVK: 0xAF, mac: 0x48, x11: "XF86AudioRaiseVolume"},
	"MediaTrackNext":     {winVK: 0xB0, mac: macNone, x11: "XF86AudioNext"},
	"MediaTrackPrevious": {winVK: 0xB1, mac: macNone, x11: "XF86AudioPrev"},
	"MediaStop":          {winVK: 0xB2, mac: macNone, x11: "XF86AudioStop"},
	"MediaPlayPause":     {winVK: 0xB3, mac: macNone, x11: "XF86AudioPlay"},
}

func knownKey(code string) bool {
	_, ok := keymap[code]
	return ok
}

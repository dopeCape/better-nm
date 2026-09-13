package desktop

import (
	_ "embed"
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/dopeCape/better-nm/internal/core"
)

//go:embed icon.png
var iconPNG []byte

func appIcon() fyne.Resource {
	if len(iconPNG) == 0 {
		return nil
	}
	return fyne.NewStaticResource("bnm.png", iconPNG)
}

// --- layout helpers ----------------------------------------------------------------

// insetLayout pads one object by explicit amounts (the spacing rhythm is
// 4/8/12/16/24; Fyne's own Padded gives only the theme padding).
type insetLayout struct{ top, right, bottom, left float32 }

func (l insetLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	if len(objs) == 0 {
		return fyne.NewSize(l.left+l.right, l.top+l.bottom)
	}
	m := objs[0].MinSize()
	return fyne.NewSize(m.Width+l.left+l.right, m.Height+l.top+l.bottom)
}

func (l insetLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(l.left, l.top))
		o.Resize(fyne.NewSize(size.Width-l.left-l.right, size.Height-l.top-l.bottom))
	}
}

// inset wraps obj with top/right/bottom/left padding.
func inset(obj fyne.CanvasObject, top, right, bottom, left float32) *fyne.Container {
	return container.New(insetLayout{top, right, bottom, left}, obj)
}

// pad wraps obj with the same padding on every side.
func pad(obj fyne.CanvasObject, p float32) *fyne.Container { return inset(obj, p, p, p, p) }

// page is the standard content pane padding.
func page(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVScroll(inset(obj, 20, 24, 24, 24))
}

// fixedLayout forces a width and/or height (0 = natural).
type fixedLayout struct{ w, h float32 }

func (l fixedLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var m fyne.Size
	if len(objs) > 0 {
		m = objs[0].MinSize()
	}
	if l.w > 0 {
		m.Width = l.w
	}
	if l.h > 0 {
		m.Height = l.h
	}
	return m
}

func (l fixedLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func fixedWidth(obj fyne.CanvasObject, w float32) *fyne.Container {
	return container.New(fixedLayout{w: w}, obj)
}

func fixedSize(obj fyne.CanvasObject, w, h float32) *fyne.Container {
	return container.New(fixedLayout{w: w, h: h}, obj)
}

// tableRowHeight is the height of one table row (or header) at the theme's text size.
const tableRowHeight = 36

// sizeTable gives a fixedSize table container the height of n rows plus the header.
func sizeTable(box *fyne.Container, rows int) {
	box.Layout = fixedLayout{h: float32(min(max(rows, 1), 12)+1)*tableRowHeight + 4}
	box.Refresh()
}

// kvLayout is a two-column key/value grid whose key column is as wide as its
// widest key; rows are pairs of objects.
type kvLayout struct{ gap float32 }

func (l kvLayout) keyWidth(objs []fyne.CanvasObject) float32 {
	var w float32
	for i := 0; i < len(objs); i += 2 {
		w = max(w, objs[i].MinSize().Width)
	}
	return w
}

func (l kvLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	kw := l.keyWidth(objs)
	var h, vw float32
	for i := 0; i+1 < len(objs); i += 2 {
		rh := max(objs[i].MinSize().Height, objs[i+1].MinSize().Height)
		h += rh
		vw = max(vw, objs[i+1].MinSize().Width)
	}
	return fyne.NewSize(kw+l.gap+vw, h)
}

func (l kvLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	kw := l.keyWidth(objs)
	var y float32
	for i := 0; i+1 < len(objs); i += 2 {
		rh := max(objs[i].MinSize().Height, objs[i+1].MinSize().Height)
		objs[i].Move(fyne.NewPos(0, y))
		objs[i].Resize(fyne.NewSize(kw, rh))
		objs[i+1].Move(fyne.NewPos(kw+l.gap, y))
		objs[i+1].Resize(fyne.NewSize(max(0, size.Width-kw-l.gap), rh))
		y += rh
	}
}

// kv is a key/value readout: muted keys, monospace values.
type kv struct {
	box    *fyne.Container
	values []*widget.Label
}

func newKV(keys ...string) *kv {
	k := &kv{box: container.New(kvLayout{gap: 16})}
	for _, key := range keys {
		kl := widget.NewLabel(key)
		kl.Importance = widget.LowImportance
		vl := widget.NewLabel("")
		vl.TextStyle = fyne.TextStyle{Monospace: true}
		vl.Truncation = fyne.TextTruncateEllipsis
		k.box.Add(kl)
		k.box.Add(vl)
		k.values = append(k.values, vl)
	}
	return k
}

func (k *kv) set(i int, v string) {
	if i < 0 || i >= len(k.values) {
		return
	}
	if v == "" {
		v = "-"
	}
	k.values[i].SetText(v)
}

func (k *kv) setAll(vs ...string) {
	for i, v := range vs {
		k.set(i, v)
	}
}

// --- text helpers ------------------------------------------------------------------

func brand() fyne.CanvasObject {
	l := widget.NewLabel("bnm")
	l.TextStyle = fyne.TextStyle{Bold: true}
	l.SizeName = theme.SizeNameHeadingText
	return l
}

// sectionTitle is a subheading with a hairline under it: sections are divided
// by lines, not boxed in cards.
func sectionTitle(text string) fyne.CanvasObject {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Bold: true}
	l.SizeName = theme.SizeNameSubHeadingText
	return container.NewVBox(inset(l, 0, 0, 0, -4), widget.NewSeparator())
}

// section stacks a title over its body with the standard rhythm.
func section(title string, body fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(sectionTitle(title), inset(body, 8, 0, 20, 0))
}

func caption(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.SizeName = theme.SizeNameCaptionText
	l.Importance = widget.LowImportance
	return l
}

func mono(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Monospace: true}
	return l
}

func bold(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Bold: true}
	return l
}

// bigNumber is a large monospace readout (Mbit/s, RTT).
func bigNumber(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	l.SizeName = theme.SizeNameHeadingText
	return l
}

// errorLabel is an inline error line, hidden while empty.
type errorLabel struct{ *widget.Label }

func newErrorLabel() *errorLabel {
	l := widget.NewLabel("")
	l.Importance = widget.DangerImportance
	l.Wrapping = fyne.TextWrapWord
	l.Hide()
	return &errorLabel{l}
}

func (e *errorLabel) set(err error) {
	if err == nil {
		e.Label.SetText("")
		e.Hide()
		return
	}
	e.Label.SetText(errText(err))
	e.Show()
}

// errText renders an error with its hint on a second line.
func errText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if hint := core.HintOf(err); hint != "" {
		if i := strings.LastIndex(msg, " ("+hint+")"); i > 0 {
			msg = msg[:i]
		}
		return msg + "\n" + hint
	}
	return msg
}

// badge is a small caption in a tinted pill for kinds and tags. It is a
// widget so its colours follow the theme variant at render time rather than
// whatever variant was current when the view was built.
type badge struct {
	widget.BaseWidget
	text string
}

func newBadge(text string) *badge {
	b := &badge{text: text}
	b.ExtendBaseWidget(b)
	return b
}

func (b *badge) CreateRenderer() fyne.WidgetRenderer {
	r := &badgeRenderer{b: b, bg: canvas.NewRectangle(color.Transparent), txt: canvas.NewText(b.text, color.Black)}
	r.bg.CornerRadius = 4
	r.txt.TextSize = theme.Size(theme.SizeNameCaptionText)
	r.Refresh()
	return r
}

type badgeRenderer struct {
	b   *badge
	bg  *canvas.Rectangle
	txt *canvas.Text
}

const badgePadX, badgePadY = 6, 2

func (r *badgeRenderer) MinSize() fyne.Size {
	m := r.txt.MinSize()
	return fyne.NewSize(m.Width+2*badgePadX, m.Height+2*badgePadY)
}

func (r *badgeRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)
	m := r.txt.MinSize()
	r.txt.Move(fyne.NewPos((size.Width-m.Width)/2, (size.Height-m.Height)/2))
	r.txt.Resize(m)
}

func (r *badgeRenderer) Refresh() {
	r.bg.FillColor = theme.Color(theme.ColorNameButton)
	r.txt.Color = theme.Color(theme.ColorNameForeground)
	r.txt.TextSize = theme.Size(theme.SizeNameCaptionText)
	r.txt.Text = r.b.text
	r.bg.Refresh()
	r.txt.Refresh()
}

func (r *badgeRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.bg, r.txt} }
func (r *badgeRenderer) Destroy()                     {}

// setCheckedSilently sets a Check without firing its OnChanged (snapping a
// switch back after a failed action must not re-run the action).
func setCheckedSilently(c *widget.Check, on bool) {
	prev := c.OnChanged
	c.OnChanged = nil
	c.SetChecked(on)
	c.OnChanged = prev
}

// --- status dot --------------------------------------------------------------------

// dot is a filled circle whose colour reports a status (real semantic state,
// the one place a coloured dot is allowed).
type dot struct {
	widget.BaseWidget
	circle *canvas.Circle
	size   float32
}

func newDot(size float32) *dot {
	d := &dot{circle: canvas.NewCircle(theme.Color(theme.ColorNameDisabled)), size: size}
	d.ExtendBaseWidget(d)
	return d
}

func (d *dot) setColor(c color.Color) {
	d.circle.FillColor = c
	d.circle.Refresh()
}

func (d *dot) CreateRenderer() fyne.WidgetRenderer {
	return &dotRenderer{d: d}
}

type dotRenderer struct{ d *dot }

func (r *dotRenderer) MinSize() fyne.Size { return fyne.NewSize(r.d.size, r.d.size) }
func (r *dotRenderer) Layout(size fyne.Size) {
	s := r.d.size
	r.d.circle.Move(fyne.NewPos((size.Width-s)/2, (size.Height-s)/2))
	r.d.circle.Resize(fyne.NewSize(s, s))
}
func (r *dotRenderer) Refresh()                     { r.d.circle.Refresh() }
func (r *dotRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.d.circle} }
func (r *dotRenderer) Destroy()                     {}

// semanticColor maps a verdict-like word to the ok/degraded/down colours.
func semanticColor(state string) color.Color {
	switch state {
	case "ok", "connected", "full", "activated", "up":
		return theme.Color(theme.ColorNameSuccess)
	case "degraded", "learning", "limited", "portal", "connecting", "activating", "needs-auth", "needs-setup":
		return theme.Color(theme.ColorNameWarning)
	case "error", "failed", "none", "down":
		return theme.Color(theme.ColorNameError)
	}
	return theme.Color(theme.ColorNameDisabled)
}

// --- signal bar --------------------------------------------------------------------

// signalBar is a thin horizontal gauge for Wi-Fi strength 0..100.
type signalBar struct {
	widget.BaseWidget
	track, fill *canvas.Rectangle
	value       float32
}

func newSignalBar() *signalBar {
	s := &signalBar{
		track: canvas.NewRectangle(theme.Color(theme.ColorNameButton)),
		fill:  canvas.NewRectangle(theme.Color(theme.ColorNamePrimary)),
	}
	s.track.CornerRadius = 2
	s.fill.CornerRadius = 2
	s.ExtendBaseWidget(s)
	return s
}

func (s *signalBar) set(strength uint8) {
	s.value = float32(strength) / 100
	s.Refresh()
}

func (s *signalBar) CreateRenderer() fyne.WidgetRenderer { return &signalRenderer{s: s} }

type signalRenderer struct {
	s    *signalBar
	size fyne.Size
}

func (r *signalRenderer) MinSize() fyne.Size { return fyne.NewSize(48, 4) }
func (r *signalRenderer) Layout(size fyne.Size) {
	r.size = size
	h := float32(4)
	y := (size.Height - h) / 2
	r.s.track.Move(fyne.NewPos(0, y))
	r.s.track.Resize(fyne.NewSize(size.Width, h))
	r.s.fill.Move(fyne.NewPos(0, y))
	r.s.fill.Resize(fyne.NewSize(size.Width*r.s.value, h))
}
func (r *signalRenderer) Refresh() {
	r.s.fill.FillColor = theme.Color(theme.ColorNamePrimary)
	r.s.track.FillColor = theme.Color(theme.ColorNameButton)
	r.Layout(r.size)
	r.s.fill.Refresh()
	r.s.track.Refresh()
}
func (r *signalRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.s.track, r.s.fill}
}
func (r *signalRenderer) Destroy() {}

// --- sparkline ---------------------------------------------------------------------

// sparkline draws a series as connected line segments over a baseline
// hairline; negative values (lost probes) break the line.
type sparkline struct {
	widget.BaseWidget
	values   []float64
	baseline float64
	height   float32
}

func newSparkline(height float32) *sparkline {
	s := &sparkline{height: height}
	s.ExtendBaseWidget(s)
	return s
}

func (s *sparkline) set(values []float64, baseline float64) {
	s.values = append(s.values[:0:0], values...)
	s.baseline = baseline
	s.Refresh()
}

func (s *sparkline) CreateRenderer() fyne.WidgetRenderer {
	r := &sparkRenderer{s: s}
	r.base = canvas.NewLine(theme.Color(theme.ColorNameSeparator))
	r.base.StrokeWidth = 1
	r.empty = caption("no samples yet")
	return r
}

type sparkRenderer struct {
	s     *sparkline
	lines []*canvas.Line
	base  *canvas.Line
	empty *widget.Label
	size  fyne.Size
}

func (r *sparkRenderer) MinSize() fyne.Size { return fyne.NewSize(160, r.s.height) }

func (r *sparkRenderer) Layout(size fyne.Size) {
	r.size = size
	r.empty.Move(fyne.NewPos(0, (size.Height-r.empty.MinSize().Height)/2))
	r.empty.Resize(r.empty.MinSize())
	vals := r.s.values
	n := len(vals)
	if n < 2 {
		return
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		if v < 0 {
			continue
		}
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if r.s.baseline > 0 {
		lo, hi = math.Min(lo, r.s.baseline), math.Max(hi, r.s.baseline)
	}
	if math.IsInf(lo, 0) {
		return
	}
	if hi-lo < 1e-9 {
		hi = lo + 1
	}
	span := hi - lo
	lo -= span * 0.1
	hi += span * 0.1
	yOf := func(v float64) float32 {
		return float32((hi-v)/(hi-lo)) * (size.Height - 2)
	}
	step := size.Width / float32(n-1)
	for i := 0; i+1 < n; i++ {
		l := r.lines[i]
		if vals[i] < 0 || vals[i+1] < 0 {
			l.Hide()
			continue
		}
		l.Show()
		l.Position1 = fyne.NewPos(float32(i)*step, yOf(vals[i])+1)
		l.Position2 = fyne.NewPos(float32(i+1)*step, yOf(vals[i+1])+1)
	}
	if r.s.baseline > 0 {
		y := yOf(r.s.baseline) + 1
		r.base.Position1 = fyne.NewPos(0, y)
		r.base.Position2 = fyne.NewPos(size.Width, y)
		r.base.Show()
	} else {
		r.base.Hide()
	}
}

func (r *sparkRenderer) Refresh() {
	n := len(r.s.values)
	want := max(0, n-1)
	for len(r.lines) < want {
		l := canvas.NewLine(theme.Color(theme.ColorNamePrimary))
		l.StrokeWidth = 1.5
		r.lines = append(r.lines, l)
	}
	for i, l := range r.lines {
		l.StrokeColor = theme.Color(theme.ColorNamePrimary)
		if i >= want {
			l.Hide()
		}
	}
	r.base.StrokeColor = theme.Color(theme.ColorNameSeparator)
	if n < 2 {
		r.empty.Show()
		r.base.Hide()
	} else {
		r.empty.Hide()
	}
	r.Layout(r.size)
	canvas.Refresh(r.s)
}

func (r *sparkRenderer) Objects() []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, len(r.lines)+2)
	objs = append(objs, r.base, r.empty)
	for _, l := range r.lines {
		objs = append(objs, l)
	}
	return objs
}

func (r *sparkRenderer) Destroy() {}

// --- banner ------------------------------------------------------------------------

// banner is the non-blocking strip under the header ("daemon unreachable").
type banner struct {
	box   *fyne.Container
	bg    *canvas.Rectangle
	label *widget.Label
}

func newBanner() *banner {
	l := widget.NewLabel("")
	l.Importance = widget.WarningImportance
	bg := canvas.NewRectangle(color.Transparent)
	b := &banner{label: l, bg: bg, box: container.NewStack(bg, inset(l, 0, 16, 0, 16))}
	b.box.Hide()
	return b
}

func (b *banner) content() fyne.CanvasObject { return b.box }

func (b *banner) show(text string) {
	b.label.SetText(text)
	b.bg.FillColor = theme.Color(theme.ColorNameHover)
	b.box.Show()
	b.box.Refresh()
}

func (b *banner) hide() {
	b.box.Hide()
}

// --- formatting --------------------------------------------------------------------

func fmtMs(v float64) string {
	if v < 0 {
		return "lost"
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}

func fmtPct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

func fmtMbps(v float64) string { return fmt.Sprintf("%.1f Mbit/s", v) }

func fmtAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Format("Jan 2, 15:04")
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm %02ds", m, int(d.Seconds())%60)
}

func joinOr(ss []string, dash string) string {
	if len(ss) == 0 {
		return dash
	}
	return strings.Join(ss, ", ")
}

func firstOr(ss []string, dash string) string {
	if len(ss) == 0 {
		return dash
	}
	return ss[0]
}

func securityName(s core.WifiSecurity) string {
	switch s {
	case core.SecOpen:
		return "Open"
	case core.SecOWE:
		return "OWE"
	case core.SecWEP:
		return "WEP"
	case core.SecWPAPSK:
		return "WPA2"
	case core.SecSAE:
		return "WPA3"
	case core.SecWPAEAP:
		return "Enterprise"
	}
	return string(s)
}

func bandName(b string) string {
	if b == "" {
		return ""
	}
	return b + " GHz"
}

// spacer is layout.NewSpacer for HBox/VBox fills.
func spacer() fyne.CanvasObject { return layout.NewSpacer() }

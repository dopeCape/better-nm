package desktop

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// bnmTheme is the palette described in the package comment: one neutral base,
// one accent, three semantic colours, and a dark variant that is its own
// palette rather than an inversion.
type bnmTheme struct{}

func newTheme() fyne.Theme { return bnmTheme{} }

func rgb(hex uint32) color.Color {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 0xff}
}

func rgba(hex uint32, a uint8) color.Color {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: a}
}

var lightColors = map[fyne.ThemeColorName]color.Color{
	theme.ColorNameBackground:          rgb(0xF3F3F1),
	theme.ColorNameHeaderBackground:    rgb(0xFBFBFA),
	theme.ColorNameMenuBackground:      rgb(0xFBFBFA),
	theme.ColorNameOverlayBackground:   rgb(0xFBFBFA),
	theme.ColorNameInputBackground:     rgb(0xFBFBFA),
	theme.ColorNameInputBorder:         rgb(0xCFCFCB),
	theme.ColorNameButton:              rgb(0xE7E7E3),
	theme.ColorNameDisabledButton:      rgb(0xECECE9),
	theme.ColorNamePressed:             rgb(0xDADAD6),
	theme.ColorNameHover:               rgb(0xE9E9E6),
	theme.ColorNameForeground:          rgb(0x212226),
	theme.ColorNameDisabled:            rgb(0x9A9C9F),
	theme.ColorNamePlaceHolder:         rgb(0x8A8C90),
	theme.ColorNamePrimary:             rgb(0x2E6FD8),
	theme.ColorNameHyperlink:           rgb(0x2E6FD8),
	theme.ColorNameFocus:               rgba(0x2E6FD8, 0x99),
	theme.ColorNameSelection:           rgb(0xD6E3F8),
	theme.ColorNameSeparator:           rgb(0xDEDED9),
	theme.ColorNameShadow:              rgba(0x000000, 0x1f),
	theme.ColorNameScrollBar:           rgba(0x000000, 0x59),
	theme.ColorNameScrollBarBackground: rgba(0x000000, 0x00),
	theme.ColorNameSuccess:             rgb(0x2F8F57),
	theme.ColorNameWarning:             rgb(0xB8791A),
	theme.ColorNameError:               rgb(0xC2413B),
	theme.ColorNameForegroundOnPrimary: rgb(0xFBFBFA),
	theme.ColorNameForegroundOnError:   rgb(0xFBFBFA),
	theme.ColorNameForegroundOnSuccess: rgb(0xFBFBFA),
	theme.ColorNameForegroundOnWarning: rgb(0xFBFBFA),
}

var darkColors = map[fyne.ThemeColorName]color.Color{
	theme.ColorNameBackground:          rgb(0x151618),
	theme.ColorNameHeaderBackground:    rgb(0x1D1E21),
	theme.ColorNameMenuBackground:      rgb(0x1D1E21),
	theme.ColorNameOverlayBackground:   rgb(0x1D1E21),
	theme.ColorNameInputBackground:     rgb(0x202124),
	theme.ColorNameInputBorder:         rgb(0x34363B),
	theme.ColorNameButton:              rgb(0x2A2C30),
	theme.ColorNameDisabledButton:      rgb(0x232427),
	theme.ColorNamePressed:             rgb(0x3A3C41),
	theme.ColorNameHover:               rgb(0x26282C),
	theme.ColorNameForeground:          rgb(0xE4E4E1),
	theme.ColorNameDisabled:            rgb(0x6F7277),
	theme.ColorNamePlaceHolder:         rgb(0x7A7D82),
	theme.ColorNamePrimary:             rgb(0x79A8F0),
	theme.ColorNameHyperlink:           rgb(0x79A8F0),
	theme.ColorNameFocus:               rgba(0x79A8F0, 0x99),
	theme.ColorNameSelection:           rgb(0x243650),
	theme.ColorNameSeparator:           rgb(0x2C2E32),
	theme.ColorNameShadow:              rgba(0x000000, 0x80),
	theme.ColorNameScrollBar:           rgba(0xFFFFFF, 0x4d),
	theme.ColorNameScrollBarBackground: rgba(0x000000, 0x00),
	theme.ColorNameSuccess:             rgb(0x5DBA7E),
	theme.ColorNameWarning:             rgb(0xD9A24A),
	theme.ColorNameError:               rgb(0xE0645E),
	theme.ColorNameForegroundOnPrimary: rgb(0x151618),
	theme.ColorNameForegroundOnError:   rgb(0x151618),
	theme.ColorNameForegroundOnSuccess: rgb(0x151618),
	theme.ColorNameForegroundOnWarning: rgb(0x151618),
}

func (bnmTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	table := lightColors
	if variant == theme.VariantDark {
		table = darkColors
	}
	if c, ok := table[name]; ok {
		return c
	}
	return theme.DefaultTheme().Color(name, variant)
}

func (bnmTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (bnmTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// Size is the type scale and spacing rhythm: 13 body, 11 caption, 15
// subheading, 20 heading; 4 px padding step; one 4 px radius.
func (bnmTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNamePadding:
		return 4
	case theme.SizeNameInnerPadding:
		return 6
	case theme.SizeNameLineSpacing:
		return 4
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius, theme.SizeNameButtonRadius, theme.SizeNameCardRadius, theme.SizeNameMenuRadius, theme.SizeNamePopupRadius, theme.SizeNameDialogRadius:
		return 4
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameScrollBar:
		return 10
	case theme.SizeNameScrollBarSmall:
		return 3
	case theme.SizeNameInputBorder:
		return 1
	}
	return theme.DefaultTheme().Size(name)
}

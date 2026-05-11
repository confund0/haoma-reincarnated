package tui

import "github.com/gdamore/tcell/v2"

const (
	StyleBreadcrumbBody = "[#8a8a8a]"
	StyleBreadcrumbNick = "[#5f0087]"
)

const (
	StylePipelineOK   = "[green]"
	StylePipelineWarn = "[yellow]"
	StylePipelineBad  = "[red]"
	StylePipelineIdle = "[gray]"

	StylePipelineText = "[-]"
)

const (
	StyleRetiredNick = "[red]"

	StyleRetiredMarker = "[red]"

	StyleRetiredDate = "[gray]"

	StyleRetiredRow = "[gray]"

	StyleReset = "[-]"

	StyleWinBarActive = "[yellow]"

	StyleWinBarUnread = "[fuchsia]"

	StyleWinBarIdle = "[#8a8a8a]"

	StyleWinBarInCall = "[red::b]"

	GlyphUnread = "*"

	GlyphInCall = "\u260e\ufe0e  "

	StyleHintText = "[#8a8a8a]"
)

var (
	ColorRetiredNick = tcell.ColorRed

	ColorRetiredRow = tcell.ColorGray

	ColorDisabledInput = tcell.ColorGray

	ColorWinBarUnread = tcell.ColorFuchsia
)

const GlyphRetired = "✕"

const (
	StylePresenceAvailable = "[green]"
	StylePresenceAway      = "[orange]"
	StylePresenceBusy      = "[red]"

	StylePresenceAccepting = "[#5f87ff]"
	StylePresenceUnknown   = "[gray]"
)

var (
	ColorPresenceAvailable = tcell.ColorGreen
	ColorPresenceAway      = tcell.ColorOrange
	ColorPresenceBusy      = tcell.ColorRed
	ColorPresenceAccepting = tcell.NewRGBColor(0x5f, 0x87, 0xff)
	ColorPresenceUnknown   = tcell.ColorGray
)

const (
	GlyphPresenceOnline    = "●"
	GlyphPresenceAccepting = "◐"
	GlyphPresenceUnknown   = "○"
)

const (
	StyleRotationActive   = "[red]"
	StyleRotationCooldown = "[yellow]"
	GlyphRotationOurs     = "↑"
	GlyphRotationTheirs   = "↓"
)

var (
	ColorRotationActive   = tcell.ColorRed
	ColorRotationCooldown = tcell.ColorYellow
)

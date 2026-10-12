package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

const (
	StyleBreadcrumbBody = "[#8a8a8a]"
	StyleBreadcrumbNick = "[#5f0087]"

	StyleCommandHint = "[#83a598]"

	StyleCallSummaryOK  = "[#98971a]"
	StyleCallSummaryBad = "[#cc241d]"

	StyleCallSummaryNeutral = "[#8a8a8a]"

	StyleOnionRotationOK  = "[#b16286]"
	StyleOnionRotationBad = "[#d79921]"
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

	StyleWinBarAlert = "[red::b]"

	GlyphAlert = "!"

	StyleHintText = "[#8a8a8a]"

	StyleNoticeOK   = "[green]"
	StyleNoticeWarn = "[yellow]"

	StyleRowStamp       = "[gray]"
	StylePendingSending = "[gray]"
	StylePendingFailed  = "[red]"
	StyleRowText        = "[white]"

	StyleUnreachable = "[#fe8019]"
)

var (
	ColorRetiredNick = tcell.ColorRed

	ColorRetiredRow = tcell.ColorGray

	ColorUnreachableRow = colorOfTag(StyleUnreachable)

	ColorDisabledInput = tcell.ColorGray

	ColorLinkWords = tcell.NewRGBColor(0xd3, 0x86, 0x9b)

	ColorWinBarUnread = tcell.ColorFuchsia
)

const GlyphRetired = "✕"

const (
	StylePresenceAvailable = "[green]"
	StylePresenceAway      = "[orange]"
	StylePresenceBusy      = "[red]"
	StylePresenceUnknown   = "[gray]"

	StylePresenceArrowOn = "[#5f87ff]"

	StylePresenceArrowOff = "[#585858]"
)

var (
	ColorPresenceAvailable = tcell.ColorGreen
	ColorPresenceAway      = tcell.ColorOrange
	ColorPresenceBusy      = tcell.ColorRed
	ColorPresenceArrowOn   = tcell.NewRGBColor(0x5f, 0x87, 0xff)
	ColorPresenceArrowOff  = tcell.NewRGBColor(0x58, 0x58, 0x58)
)

const (
	GlyphPresenceUp   = "↑"
	GlyphPresenceDown = "↓"
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

const (
	StyleLinkStress = "[#d79921]"
	StyleLinkWords  = "[yellow]"
	StyleLinkOK     = "[green]"
	StyleLinkBad    = "[red]"
	StyleLinkDim    = "[gray]"
	StyleLinkBody   = "[white]"

	StyleTorSlow = "[#fe8019]"

	StyleLinkLogLabel = "[#d79921]"
	StyleLinkLogLine  = "[gray]"
	StyleLinkLogWait  = "[#b16286]"
)

const (
	StyleInvitationLive = "[#458588]"
	StyleInvitationOK   = StyleCallSummaryOK
	StyleInvitationBad  = StyleCallSummaryBad
	StyleInvitationDim  = StyleBreadcrumbBody
)

const (
	StyleEraseWarn   = StyleLinkBad
	StyleEraseStress = StyleLinkStress
	StyleEraseBody   = StyleLinkBody
	StyleEraseDim    = StyleLinkDim
)

const (
	StylePairJoinBad  = StyleLinkBad
	StylePairJoinBody = StyleLinkBody
	StylePairJoinDim  = StyleLinkDim
)

var (
	ColorInvitationLive = colorOfTag(StyleInvitationLive)
	ColorInvitationOK   = colorOfTag(StyleInvitationOK)
	ColorInvitationBad  = colorOfTag(StyleInvitationBad)
	ColorInvitationDim  = colorOfTag(StyleInvitationDim)
)

const (
	StyleContactsSection = StyleBreadcrumbBody
	StyleContactsMarked  = StyleLinkWords
	StyleContactsPairing = StyleInvitationLive
	StyleContactsOK      = StyleInvitationOK
	StyleContactsBad     = StyleInvitationBad

	StyleContactsName   = StyleLinkBody
	StyleContactsCursor = StyleLinkWords
)

var (
	ColorContactsSection = colorOfTag(StyleContactsSection)
	ColorContactsMarked  = colorOfTag(StyleContactsMarked)
	ColorContactsPairing = colorOfTag(StyleContactsPairing)
	ColorContactsOK      = colorOfTag(StyleContactsOK)
	ColorContactsBad     = colorOfTag(StyleContactsBad)
	ColorContactsName    = colorOfTag(StyleContactsName)
	ColorContactsCursor  = colorOfTag(StyleContactsCursor)
)

const GlyphContactMarked = "*"

const GlyphContactCursor = ">"

func colorOfTag(tag string) tcell.Color {
	return tcell.GetColor(strings.Trim(tag, "[]"))
}

const (
	StyleAnswerHeading = StyleLinkWords
	StyleAnswerGone    = StyleLinkBad
	StyleAnswerHint    = StyleLinkDim
)

const StyleCheckboxCursor = StyleContactsCursor

var ColorCheckboxCursor = colorOfTag(StyleCheckboxCursor)

const (
	StyleHelpCommand = "[yellow]"
	StyleHelpBody    = "[white]"
)

const (
	StyleSearchHighlight        = "[black:#D79921]"
	StyleSearchHighlightCurrent = "[white:#9D0006]"
	StyleSearchHighlightOff     = "[-:-][white]"
)

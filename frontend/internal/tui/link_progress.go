package tui

import "time"

var spinnerFrames = []rune{'\u280b', '\u2819', '\u2839', '\u2838', '\u283c', '\u2834', '\u2826', '\u2827', '\u2807', '\u280f'}

const linkCeremonyTick = 150 * time.Millisecond

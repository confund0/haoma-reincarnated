//go:build !faults

package main

import (
	"log/slog"

	"haoma-frontend/internal/syncrail"
)

type seededFaults struct{}

func seededFaultsFromEnv() seededFaults { return seededFaults{} }

func siblingOfferIgnored(*daemon, *slog.Logger) bool { return false }

func gapFillFetchHeldAtTake(*daemon, *slog.Logger, string) bool { return false }

func gapFillFetchHeldAtSweep(*daemon, *slog.Logger) bool { return false }

func fileRequestIgnored(*daemon, *slog.Logger) bool { return false }

func contactDeletePushDropped(*daemon, syncrail.DeletedContact) bool { return false }

func earlyReceiptSkipped(*daemon, *slog.Logger) bool { return false }

func earlyReceiptIgnored(*daemon, *slog.Logger, string, string) bool { return false }

func readDestroySaltDropFault() {}

func destroySaltDropped(*slog.Logger) bool { return false }

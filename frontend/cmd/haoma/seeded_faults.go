//go:build faults

package main

import (
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"haoma-frontend/internal/syncrail"
)

type seededFaults struct {
	ignoreSiblingOffer bool

	ignoreFileRequest bool

	holdGapFillFetch bool

	dropContactDeletePush bool

	skipEarlyReceipt bool

	ignoreEarlyReceipt bool
	receiptsSeen       sync.Map
}

func seededFaultsFromEnv() seededFaults {
	return seededFaults{
		ignoreSiblingOffer:    ignoreSiblingOfferFromEnv(),
		ignoreFileRequest:     ignoreFileRequestFromEnv(),
		holdGapFillFetch:      holdGapFillFetchFromEnv(),
		dropContactDeletePush: dropContactDeletePushFromEnv(),
		skipEarlyReceipt:      skipEarlyReceiptFromEnv(),
		ignoreEarlyReceipt:    ignoreEarlyReceiptFromEnv(),
	}
}

func faultSettingOn(env, onLine string) bool {
	raw := os.Getenv(env)
	if raw == "" {
		return false
	}
	if raw != "1" {
		slog.Warn("seeded fault setting ignored (only \"1\" turns it on)",
			slog.String("env", env),
		)
		return false
	}
	slog.Warn(onLine, slog.String("env", env))
	return true
}

const ignoreSiblingOfferEnv = "HAOMA_FAULT_IGNORE_SIBLING_OFFER"

func ignoreSiblingOfferFromEnv() bool {
	return faultSettingOn(ignoreSiblingOfferEnv,
		"seeded fault on: this device ignores every sibling offer, so no file I send from my other device reaches it")
}

func siblingOfferIgnored(d *daemon, logger *slog.Logger) bool {
	if !d.ignoreSiblingOffer {
		return false
	}
	logger.Warn("sibling offer: ignored by the seeded fault; the row lands with no transfer",
		slog.String("reason", "seeded_fault"),
		slog.String("env", ignoreSiblingOfferEnv),
	)
	return true
}

const holdGapFillFetchEnv = "HAOMA_FAULT_HOLD_GAPFILL_FETCH"

const holdGapFillFetchOnLine = "seeded fault on: this device takes a sibling offer on gap-fill and fetches nothing for it, so such a file stays pending"

func holdGapFillFetchFromEnv() bool {
	return faultSettingOn(holdGapFillFetchEnv, holdGapFillFetchOnLine)
}

func gapFillFetchHeldAtTake(d *daemon, logger *slog.Logger, stagingID string) bool {
	if !d.holdGapFillFetch {
		return false
	}
	logger.Warn("sibling offer on gap-fill: taken; the seeded fault holds its fetch",
		slog.String("staging_id", stagingID),
		slog.String("reason", "seeded_fault"),
		slog.String("env", holdGapFillFetchEnv),
		slog.String("outcome", "fetch_held"),
	)
	return true
}

func gapFillFetchHeldAtSweep(d *daemon, logger *slog.Logger) bool {
	if !d.holdGapFillFetch {
		return false
	}
	logger.Debug("sibling fetch sweep: the seeded fault holds this fetch",
		slog.String("outcome", "fetch_held"),
	)
	return true
}

const ignoreFileRequestEnv = "HAOMA_FAULT_IGNORE_FILE_REQUEST"

func ignoreFileRequestFromEnv() bool {
	return faultSettingOn(ignoreFileRequestEnv,
		"seeded fault on: this device serves no file request, so no file reaches my other device by gap-fill from here")
}

func fileRequestIgnored(d *daemon, logger *slog.Logger) bool {
	if !d.ignoreFileRequest {
		return false
	}
	logger.Warn("file request: ignored by the seeded fault; nothing is served",
		slog.String("reason", "seeded_fault"),
		slog.String("env", ignoreFileRequestEnv),
		slog.String("outcome", "not_served"),
	)
	return true
}

const dropContactDeletePushEnv = "HAOMA_FAULT_DROP_CONTACT_DELETE_PUSH"

const dropContactDeletePushOnLine = "seeded fault on: a contact deleted on this device is not pushed to my other device, so only the sweep can carry the delete"

const contactDeletePushDroppedLine = "seeded fault: the contact delete was not pushed to my other device"

func dropContactDeletePushFromEnv() bool {
	return faultSettingOn(dropContactDeletePushEnv, dropContactDeletePushOnLine)
}

func contactDeletePushDropped(d *daemon, deleted syncrail.DeletedContact) bool {
	if !d.dropContactDeletePush {
		return false
	}
	slog.Warn(contactDeletePushDroppedLine,
		slog.String("env", dropContactDeletePushEnv),
		slog.String("fingerprint", deleted.Fingerprint),
	)
	return true
}

const skipEarlyReceiptEnv = "HAOMA_FAULT_SKIP_EARLY_RECEIPT"

func skipEarlyReceiptFromEnv() bool {
	return faultSettingOn(skipEarlyReceiptEnv,
		"seeded fault on: no receipt is sent when a contact's file offer arrives; the key waits for the download")
}

func earlyReceiptSkipped(d *daemon, logger *slog.Logger) bool {
	if !d.skipEarlyReceipt {
		return false
	}
	logger.Debug("file receipt: early receipt skipped",
		slog.String("reason", "seeded_fault"),
		slog.String("env", skipEarlyReceiptEnv),
	)
	return true
}

const ignoreEarlyReceiptEnv = "HAOMA_FAULT_IGNORE_EARLY_RECEIPT"

func ignoreEarlyReceiptFromEnv() bool {
	return faultSettingOn(ignoreEarlyReceiptEnv,
		"seeded fault on: the first receipt from each device for each file gets no key")
}

func earlyReceiptIgnored(d *daemon, logger *slog.Logger, msgID, deviceID string) bool {
	if !d.ignoreEarlyReceipt || !firstReceiptFor(d, msgID, deviceID) {
		return false
	}
	logger.Debug("file receipt: ignored",
		slog.String("reason", "seeded_fault"),
		slog.String("env", ignoreEarlyReceiptEnv),
	)
	return true
}

func firstReceiptFor(d *daemon, msgID, deviceID string) bool {
	_, seen := d.receiptsSeen.LoadOrStore(msgID+"|"+deviceID, struct{}{})
	return !seen
}

const dropFirstDestroySaltEnv = "HAOMA_FAULT_DROP_FIRST_DESTROY_SALT"

const dropFirstDestroySaltOnLine = "seeded fault on: this device drops the first destroy PIN salt my other device sends, so only the sweep can bring it"

const destroySaltDroppedLine = "seeded fault: the first destroy PIN salt from my other device was dropped"

type firstSaltDrop struct{ spent atomic.Bool }

func (f *firstSaltDrop) take() bool {
	return f != nil && f.spent.CompareAndSwap(false, true)
}

var saltDropFault struct {
	once sync.Once
	f    *firstSaltDrop
}

func destroySaltDropFault() *firstSaltDrop {
	saltDropFault.once.Do(func() { saltDropFault.f = dropFirstDestroySaltFromEnv() })
	return saltDropFault.f
}

func readDestroySaltDropFault() {
	destroySaltDropFault()
}

func destroySaltDropped(logger *slog.Logger) bool {
	if !destroySaltDropFault().take() {
		return false
	}
	logger.Warn(destroySaltDroppedLine,
		slog.String("env", dropFirstDestroySaltEnv),
	)
	return true
}

func dropFirstDestroySaltFromEnv() *firstSaltDrop {
	if !faultSettingOn(dropFirstDestroySaltEnv, dropFirstDestroySaltOnLine) {
		return nil
	}
	return &firstSaltDrop{}
}

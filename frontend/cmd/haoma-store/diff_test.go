package main

import (
	"errors"
	"os"
	"testing"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/store"
)

func TestDiff_IdenticalStoresReportNoDifference(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-hello", "see you at six", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-hello", "see you at six", events.DeliverySent, at(1))
	a.textIn(chatA, "msg-reply", "I love you", at(2))
	b.textIn(chatB, "msg-reply", "I love you", at(2))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.mustAgreeCount() > 0 {
		t.Fatalf("two identical stores reported a difference\n%s", report)
	}
	if res.conversations.matched != 1 {
		t.Fatalf("matched %d conversations, want 1\n%s", res.conversations.matched, report)
	}
	if res.messages.matched != 2 {
		t.Fatalf("matched %d messages, want 2\n%s", res.messages.matched, report)
	}
	mustContain(t, report, "hold the same thing everywhere this tool")
}

func TestDiff_JoinsOnThePersonWhenChatIDsDiffer(t *testing.T) {
	a, b, chatA, chatB := pairOfDevicesDifferentChatIDs(t)
	if chatA == chatB {
		t.Fatal("the fixture must give the two devices different chat ids")
	}
	a.textOut(chatA, "msg-hello", "hello", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-hello", "hello", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.conversations.matched != 1 {
		t.Fatalf("matched %d conversations, want 1 — the join key is the fingerprint\n%s",
			res.conversations.matched, report)
	}
	if res.conversations.onlyA != 0 || res.conversations.onlyB != 0 {
		t.Fatalf("conversations reported on one side only: a=%d b=%d\n%s",
			res.conversations.onlyA, res.conversations.onlyB, report)
	}

	mustContain(t, report, "backfill has not carried one across")
}

func TestDiff_MessagePresentOnOneSideOnly(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-shared", "shared", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-shared", "shared", events.DeliverySent, at(1))
	a.textOut(chatA, "msg-lonely", "only on the first device", events.DeliverySent, at(2))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.onlyA != 1 {
		t.Fatalf("messages only in a = %d, want 1\n%s", res.messages.onlyA, report)
	}
	if res.messages.onlyB != 0 {
		t.Fatalf("messages only in b = %d, want 0\n%s", res.messages.onlyB, report)
	}
	if res.messages.matched != 1 {
		t.Fatalf("matched %d messages, want 1\n%s", res.messages.matched, report)
	}
	mustContain(t, report, "only in a")
	mustContain(t, report, "message msg-lonely")

	if res.mustAgreeCount() != 0 {
		t.Fatalf("a row on one side only was counted as divergence\n%s", report)
	}
	if got := verdictExit(res); got != exitReview {
		t.Fatalf("exit %d, want %d\n%s", got, exitReview, report)
	}
	mustContain(t, report, "may be a fault, and")
}

func TestDiff_SameMessageDifferentReadState(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textIn(chatA, "msg-read-here", "did you see this", at(1))
	b.textIn(chatB, "msg-read-here", "did you see this", at(1))
	a.markRead("msg-read-here", at(3))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.differ != 1 {
		t.Fatalf("%d messages differ, want 1\n%s", res.messages.differ, report)
	}
	if res.messages.matched != 1 {
		t.Fatalf("matched %d messages, want 1\n%s", res.messages.matched, report)
	}
	mustContain(t, report, "read at")
	mustContain(t, report, "message msg-read-here")
}

func TestDiff_SameMessageDifferentDeliveryState(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-sent", "on its way", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-sent", "on its way", events.DeliveryRead, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.differ != 1 {
		t.Fatalf("%d messages differ, want 1\n%s", res.messages.differ, report)
	}
	mustContain(t, report, "delivery state")
	mustContain(t, report, "sent")
	mustContain(t, report, "read")
}

func TestDiff_FileRowDifferentTransferState(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.attachment(chatA, "msg-file", "holiday.jpg", "look at this", string(files.StateDownloading), at(1))
	b.attachment(chatB, "msg-file", "holiday.jpg", "look at this", string(files.StateReady), at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.files.differ != 1 {
		t.Fatalf("%d file rows differ, want 1\n%s", res.files.differ, report)
	}
	if res.files.matched != 1 {
		t.Fatalf("matched %d file rows, want 1\n%s", res.files.matched, report)
	}
	mustContain(t, report, "transfer state")
	mustContain(t, report, "downloading")
	mustContain(t, report, "ready")
}

func TestDiff_RefusesAStoreWhoseDaemonHoldsIt(t *testing.T) {
	a, b, _, _ := pairOfDevices(t)
	dirA := a.close()
	dirB := b.close()

	daemon, err := store.Unlock(dirB, fixturePassphrase)
	if err != nil {
		t.Fatalf("hold the store: %v", err)
	}
	defer func() { _ = daemon.Lock() }()

	t.Setenv(envPassphrase, fixturePassphrase)
	if _, err := openSide(dirB, "b", "", t.TempDir()); err == nil {
		t.Fatal("opened a store another process holds")
	} else {
		mustContain(t, err.Error(), "its daemon is running")
		mustContain(t, err.Error(), "no flag to override")
	}

	sideA, err := openSide(dirA, "a", "", t.TempDir())
	if err != nil {
		t.Fatalf("the store nobody holds should still open: %v", err)
	}
	sideA.close()
}

func TestReadOnly_RefusesADirectoryThatIsNotAStore(t *testing.T) {
	dir := t.TempDir()
	_, err := store.UnlockReadOnly(dir, fixturePassphrase)
	if !errors.Is(err, store.ErrNoMeta) {
		t.Fatalf("err = %v, want ErrNoMeta", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("the read-only open created %d entries in a directory it was only reading", len(entries))
	}
}

func TestDiff_TwoStoresOfDifferentPeopleDifferEverywhere(t *testing.T) {
	a := newStore(t, "alice")
	a.pairedDirectly("pairing-a1", "aaaa1111aaaa1111aaaa1111aaaa1111", "Bob")
	dirA := a.close()

	b := newStore(t, "eddie")
	b.pairedDirectly("pairing-b1", "bbbb2222bbbb2222bbbb2222bbbb2222", "Carol")
	dirB := b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if !res.comparedAnything() {
		t.Fatalf("both stores hold conversations that can be named; that is comparable\n%s", report)
	}

	if res.mustAgreeCount() != 0 {
		t.Fatalf("nothing here is a field that must agree\n%s", report)
	}
	if res.oneSidedCount() == 0 {
		t.Fatalf("every row should be reported as one-sided\n%s", report)
	}
	if got := verdictExit(res); got != exitReview {
		t.Fatalf("exit code %d, want %d\n%s", got, exitReview, report)
	}
}

func TestDiff_TwoEmptyStoresCompareNothing(t *testing.T) {
	a := newStore(t, "alice")
	dirA := a.close()
	b := newStore(t, "alice-linked")
	dirB := b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.comparedAnything() {
		t.Fatalf("two empty stores reported a comparison\n%s", report)
	}
	mustContain(t, report, "VERDICT: nothing was compared")
	if got := verdictExit(res); got != exitIncomplete {
		t.Fatalf("exit code %d, want %d (not %d, which would mean they agreed)\n%s",
			got, exitIncomplete, exitAgreed, report)
	}
}

func TestDiff_ExitCodes(t *testing.T) {
	t.Run("agreed is 0", func(t *testing.T) {
		a, b, chatA, chatB := pairOfDevices(t)
		a.textOut(chatA, "msg", "hello", events.DeliverySent, at(1))
		b.textOut(chatB, "msg", "hello", events.DeliverySent, at(1))
		dirA, dirB := a.close(), b.close()
		sideA, sideB := openSides(t, dirA, dirB)
		res, report := runDiffInto(t, sideA, sideB)
		if got := verdictExit(res); got != exitAgreed {
			t.Fatalf("exit %d, want %d\n%s", got, exitAgreed, report)
		}
	})

	t.Run("a gap with no difference is 3, never 0", func(t *testing.T) {
		a := newStore(t, "alice")
		chatA := a.pairedButPersonUnknown("pairing-a1", "Bob")
		a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))
		b := newStore(t, "alice-linked")
		chatB := b.pairedButPersonUnknown("pairing-b1", "Bob")
		b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))
		dirA, dirB := a.close(), b.close()
		sideA, sideB := openSides(t, dirA, dirB)
		res, report := runDiffInto(t, sideA, sideB)
		if res.mustAgreeCount() > 0 {
			t.Fatalf("no difference expected here\n%s", report)
		}
		if got := verdictExit(res); got != exitIncomplete {
			t.Fatalf("exit %d, want %d\n%s", got, exitIncomplete, report)
		}
	})

	t.Run("a difference outranks a gap", func(t *testing.T) {
		a := newStore(t, "alice")
		chatA := a.pairedButPersonUnknown("pairing-a1", "Bob")
		a.textOut(chatA, "msg-shared", "hello", events.DeliveryRead, at(1))
		b := newStore(t, "alice-linked")
		chatB := b.pairedButPersonUnknown("pairing-b1", "Bob")
		b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))
		dirA, dirB := a.close(), b.close()
		sideA, sideB := openSides(t, dirA, dirB)
		res, report := runDiffInto(t, sideA, sideB)
		if len(res.notComparable()) == 0 {
			t.Fatalf("this fixture is supposed to have a gap\n%s", report)
		}
		if got := verdictExit(res); got != exitDiffers {
			t.Fatalf("exit %d, want %d\n%s", got, exitDiffers, report)
		}
	})
}

func TestDiff_ContactSet(t *testing.T) {
	a, b, _, _ := pairOfDevices(t)
	a.pairedDirectly("pairing-a2", "cccc3333cccc3333cccc3333cccc3333", "Carol")
	a.linkDevice("a-third-device")
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.contacts.onlyA != 1 {
		t.Fatalf("contacts only in a = %d, want 1\n%s", res.contacts.onlyA, report)
	}
	mustContain(t, report, "## contacts")
}

func TestDiff_ContactAliasIsCompared(t *testing.T) {
	a, b, _, _ := pairOfDevices(t)
	a.alias("pairing-a1", "Bob from work", at(1))
	b.alias("pairing-b1", "Bob the neighbour", at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.contacts.differ != 1 {
		t.Fatalf("%d contacts differ, want 1\n%s", res.contacts.differ, report)
	}
	mustContain(t, report, "name I gave them")
	mustContain(t, report, "Bob from work")
	mustContain(t, report, "Bob the neighbour")
}

func TestDiff_DoesNotCompareALocalConversationSetting(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-hello", "hello", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-hello", "hello", events.DeliverySent, at(1))
	a.disableReadReceipts(chatA)
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.mustAgreeCount() > 0 {
		t.Fatalf("a local setting was reported as a difference\n%s", report)
	}
	mustContain(t, report, "never converged")
}

func TestDiff_DoesNotCompareTheMirroredMarker(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-mirrored", "hello", events.DeliverySent, at(1))
	b.mirroredTextOut(chatB, "msg-mirrored", "hello", at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.matched != 1 {
		t.Fatalf("matched %d messages, want 1\n%s", res.messages.matched, report)
	}
	if res.messages.differ != 0 {
		t.Fatalf("%d messages differ, want 0 — only the mirrored marker is unequal\n%s",
			res.messages.differ, report)
	}
	mustContain(t, report, "mirrored-from-sibling marker")
}

func TestDiff_LinkedDevicesAreReadOutNotCompared(t *testing.T) {
	a, b, _, _ := pairOfDevices(t)

	a.linkDevice("id-a-uses-for-b")
	b.linkDevice("id-b-uses-for-a")
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.mustAgreeCount() > 0 {
		t.Fatalf("a healthy linked pair was reported as divergent\n%s", report)
	}
	mustContain(t, report, "linked devices: read out, not compared")
	mustContain(t, report, "cannot be matched")
	mustContain(t, report, "a records 1 sibling(s)")
	mustContain(t, report, "b records 1 sibling(s)")

	mustNotContain(t, report, "only in a   device")
	mustNotContain(t, report, "only in b   device")
}

func TestDiff_LinkedDeviceReadoutFlagsASideThatRecordsNone(t *testing.T) {
	a, b, _, _ := pairOfDevices(t)
	a.linkDevice("id-a-uses-for-b")
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	_, report := runDiffInto(t, sideA, sideB)

	mustContain(t, report, "FINDING:")
	mustContain(t, report, "b records no siblings at all while a records 1")
	mustContain(t, report, "does not know it is linked")
}

func TestDiff_AFailedConversationJoinDoesNotSilenceTheRest(t *testing.T) {

	a := newStore(t, "alice")
	chatA := a.pairedButPersonUnknown("pairing-a1", "Bob")
	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))

	b := newStore(t, "alice-linked")
	chatB := b.pairedButPersonUnknown("pairing-b1", "Bob")
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))

	dirA, dirB := a.close(), b.close()
	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.matched != 1 {
		t.Fatalf("matched %d messages, want 1\n%s", res.messages.matched, report)
	}
	if !res.comparedAnything() {
		t.Fatalf("messages matched, so something WAS compared\n%s", report)
	}

	if got := res.conversations.state(); got != joinFailed {
		t.Fatalf("conversation join state = %v, want joinFailed\n%s", got, report)
	}

	mustNotContain(t, report, "VERDICT: nothing was compared")
	mustContain(t, report, "everything that could be compared agrees, but some rows could")
	mustContain(t, report, "=== what could NOT be compared, and why ===")
	mustContain(t, report, "identity fingerprint")

	mustNotContain(t, report, "hold the same thing everywhere this tool")
}

func TestDiff_ACategoryNeitherSideHoldsIsNotAGap(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if got := res.files.state(); got != joinNothingThere {
		t.Fatalf("file-row join state = %v, want joinNothingThere\n%s", got, report)
	}
	if len(res.notComparable()) != 0 {
		t.Fatalf("a category nobody holds was reported as a gap: %v\n%s",
			res.notComparable(), report)
	}
	mustContain(t, report, "hold the same thing everywhere this tool")
	mustNotContain(t, report, "could NOT be compared")
}

func TestDiff_AOneSidedCategoryIsAResultNotAGap(t *testing.T) {
	a, b, chatA, _ := pairOfDevices(t)
	a.attachment(chatA, "msg-file", "holiday.jpg", "the beach", string(files.StateReady), at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if got := res.files.state(); got != joinOneSided {
		t.Fatalf("file-row join state = %v, want joinOneSided\n%s", got, report)
	}
	if len(res.notComparable()) != 0 {
		t.Fatalf("a one-sided category was reported as a gap\n%s", report)
	}
	if res.oneSidedCount() == 0 {
		t.Fatalf("the file row should be reported as one-sided\n%s", report)
	}
	if res.mustAgreeCount() != 0 {
		t.Fatalf("a row on one side only is not divergence\n%s", report)
	}
}

func TestDiff_JoinsAPairedDeviceToItsBackfilledSibling(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	if chatA != chatB {
		t.Fatalf("backfill adopts the chat id; fixture gave %s and %s", chatA, chatB)
	}

	aChats, err := readChats(a.st)
	if err != nil {
		t.Fatal(err)
	}
	if aChats[0].Fingerprint != "" {
		t.Fatal("a paired directly, so its conversation must carry no person marker")
	}
	bChats, err := readChats(b.st)
	if err != nil {
		t.Fatal(err)
	}
	if bChats[0].Fingerprint == "" {
		t.Fatal("b received the conversation from its sibling, so it must carry the person")
	}

	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.conversations.matched != 1 {
		t.Fatalf("matched %d conversations, want 1\n%s", res.conversations.matched, report)
	}
	if res.conversations.onlyA != 0 || res.conversations.onlyB != 0 {
		t.Fatalf("conversations reported one-sided: a=%d b=%d\n%s",
			res.conversations.onlyA, res.conversations.onlyB, report)
	}
	if res.mustAgreeCount() > 0 {
		t.Fatalf("a healthy linked pair reported a difference\n%s", report)
	}
	if got := verdictExit(res); got != exitAgreed {
		t.Fatalf("exit %d, want %d\n%s", got, exitAgreed, report)
	}
}

func TestDiff_JoinsWhenOneSideHasNoPairingYet(t *testing.T) {
	a, b, chatA, _ := pairOfDevicesBeforeRepair(t)
	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.conversations.matched != 1 {
		t.Fatalf("matched %d conversations, want 1\n%s", res.conversations.matched, report)
	}
	mustContain(t, report, "Bob")
}

func TestDiff_AnUnplaceableConversationIsNotCalledOneSided(t *testing.T) {
	a := newStore(t, "alice")
	a.pairedButPersonUnknown("pairing-a1", "Bob")
	dirA := a.close()
	b := newStore(t, "alice-linked")
	b.pairedButPersonUnknown("pairing-b1", "Bob")
	dirB := b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.conversations.onlyA != 0 || res.conversations.onlyB != 0 {
		t.Fatalf("an unplaceable conversation was called one-sided: a=%d b=%d\n%s",
			res.conversations.onlyA, res.conversations.onlyB, report)
	}
	if res.conversations.unnameable != 2 {
		t.Fatalf("%d conversations could not be placed, want 2\n%s",
			res.conversations.unnameable, report)
	}
	mustContain(t, report, "cannot name the person they belong to")
	mustContain(t, report, "would be a guess")
	if res.mustAgreeCount() > 0 {
		t.Fatalf("not being able to place a conversation is not a difference\n%s", report)
	}
}

func TestFind_NamesAConversationOnADirectlyPairedStore(t *testing.T) {
	a := newStore(t, "alice")
	chatID := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")
	a.textOut(chatID, "msg-one", "see you at six", events.DeliverySent, at(1))
	dir := a.close()

	s := openOne(t, dir)
	_, report := runFindInto(t, []*side{s}, findFilter{text: "six"}, false)

	mustContain(t, report, "Bob")
	mustNotContain(t, report, "conversation "+shortID(string(chatID)))
}

func TestFind_ByConversationNameOnADirectlyPairedStore(t *testing.T) {
	a := newStore(t, "alice")
	chatID := a.pairedDirectly("pairing-a1", bobFingerprint, "Bob")
	a.textOut(chatID, "msg-one", "see you at six", events.DeliverySent, at(1))
	dir := a.close()

	s := openOne(t, dir)
	res, report := runFindInto(t, []*side{s}, findFilter{conv: "Bob"}, false)
	if len(res.hits) != 1 {
		t.Fatalf("%d hits for the contact's name, want 1\n%s", len(res.hits), report)
	}
}

func TestDiff_JoinsOnTheChatIDWhenThePersonCannotBeResolved(t *testing.T) {
	a := newStore(t, "alice")
	chatA := a.pairedButPersonUnknown("pairing-a1", "Bob")
	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))

	b := newStore(t, "alice-linked")
	chatB := b.receivedFromSibling(chatA, bobFingerprint, "", "")
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))

	dirA, dirB := a.close(), b.close()
	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.conversations.matched != 1 {
		t.Fatalf("matched %d conversations, want 1 — the chat id is the only key left\n%s",
			res.conversations.matched, report)
	}
	if res.conversations.unnameable != 0 {
		t.Fatalf("%d conversations went unplaced, want 0\n%s",
			res.conversations.unnameable, report)
	}
}

func TestDiff_AHealthyPairDoesNotVerdictAsDiverged(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)

	a.textIn(chatA, "msg-shared", "see you at six", at(0))
	b.textIn(chatB, "msg-shared", "see you at six", atSeconds(1))

	a.markRead("msg-shared", at(2))
	a.markReadCursor(chatA, at(2))
	b.markRead("msg-shared", atSeconds(121))
	b.markReadCursor(chatB, atSeconds(121))

	a.attachment(chatA, "msg-file-1", "one.jpg", "", string(files.StateReady), at(3))
	a.attachment(chatA, "msg-file-2", "two.jpg", "", string(files.StateReady), at(4))

	dirA, dirB := a.close(), b.close()
	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.mustAgreeCount() != 0 {
		t.Fatalf("a healthy pair reported %d fields that must agree as differing\n%s",
			res.mustAgreeCount(), report)
	}
	if got := verdictExit(res); got == exitDiffers {
		t.Fatalf("a healthy pair exited as a difference\n%s", report)
	}
	mustNotContain(t, report, "VERDICT: these two stores have not converged")

	if len(res.tolerated) == 0 {
		t.Fatalf("the clock-stamped fields inside the tolerance were not recorded\n%s", report)
	}
	mustContain(t, report, "ignored: inside the clock tolerance, which is 5 seconds")
	mustContain(t, report, "second(s) apart")

	if res.skewCount() != 0 {
		t.Fatalf("nothing here is outside the tolerance\n%s", report)
	}
	if res.oneSidedCount() == 0 {
		t.Fatalf("the two file rows should be reported as one-sided\n%s", report)
	}
	if got := verdictExit(res); got != exitReview {
		t.Fatalf("exit %d, want %d\n%s", got, exitReview, report)
	}
}

func TestDiff_ClockSkewInsideTheToleranceIsNotADifference(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textIn(chatA, "msg-shared", "hello", at(0))

	b.textIn(chatB, "msg-shared", "hello", atSeconds(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.mustAgreeCount() != 0 || res.skewCount() != 0 {
		t.Fatalf("a gap of one second is inside the tolerance\n%s", report)
	}
	if got := verdictExit(res); got != exitAgreed {
		t.Fatalf("exit %d, want %d\n%s", got, exitAgreed, report)
	}
	mustContain(t, report, "VERDICT: these two stores hold the same thing everywhere this tool")

	if len(res.tolerated) != 1 {
		t.Fatalf("%d tolerated, want 1\n%s", len(res.tolerated), report)
	}
	mustContain(t, report, "shown at")
}

func TestDiff_ClockSkewOutsideTheToleranceIsNamedButNotDivergence(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textIn(chatA, "msg-shared", "hello", at(0))

	b.textIn(chatB, "msg-shared", "hello", atSeconds(7))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.skewCount() != 1 {
		t.Fatalf("%d clock-stamped fields outside the tolerance, want 1\n%s", res.skewCount(), report)
	}
	if res.mustAgreeCount() != 0 {
		t.Fatalf("drift is not a field that must agree\n%s", report)
	}
	if got := verdictExit(res); got != exitReview {
		t.Fatalf("exit %d, want %d — drift is worth reading, not divergence\n%s",
			got, exitReview, report)
	}
	mustContain(t, report, "clock skew, wider than the clock tolerance")
	mustContain(t, report, "seconds apart)")
	mustNotContain(t, report, "VERDICT: these two stores have not converged")
}

func TestDiff_AnUnsetStampAgainstASetOneIsNotClockSkew(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textIn(chatA, "msg-shared", "hello", at(0))
	b.textIn(chatB, "msg-shared", "hello", at(0))
	a.markRead("msg-shared", at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.mustAgreeCount() == 0 {
		t.Fatalf("read on one device and not the other is a real finding\n%s", report)
	}
	if res.skewCount() != 0 {
		t.Fatalf("an unset stamp was reported as drift\n%s", report)
	}
	if got := verdictExit(res); got != exitDiffers {
		t.Fatalf("exit %d, want %d\n%s", got, exitDiffers, report)
	}
	mustContain(t, report, "which no clock explains")
}

func TestDiff_ARealDifferenceSurvivesAlongsideToleratedSkew(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-shared", "hello", events.DeliveryRead, at(0))
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, atSeconds(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if len(res.tolerated) == 0 {
		t.Fatalf("the one-second gap should have been tolerated\n%s", report)
	}
	if res.mustAgreeCount() != 1 {
		t.Fatalf("%d fields that must agree differ, want 1\n%s", res.mustAgreeCount(), report)
	}
	if got := verdictExit(res); got != exitDiffers {
		t.Fatalf("exit %d, want %d\n%s", got, exitDiffers, report)
	}
	mustContain(t, report, "delivery state")
}

func TestDiff_TheToleranceIsFiveSeconds(t *testing.T) {
	if clockTolerance != 5 {
		t.Fatalf("the clock tolerance is %d seconds, not 5.\n"+
			"Widening it hides drift the project's own rule says to warn about; "+
			"narrowing it reports skew every healthy pair has. "+
			"If this is deliberate, update the tests that use literal gaps of 1 and 7 seconds.",
			clockTolerance)
	}
}

func TestDiff_RefusesTwoDifferentPeoplesStores(t *testing.T) {

	a := newStoreOwnedBy(t, "alice", aliceIdentity)
	chatA := a.pairedDirectly("pairing-a1", "eeee0000eeee0000eeee0000eeee0000", "Eddie")
	a.textOut(chatA, "msg-exchanged", "see you at six", events.DeliverySent, at(1))
	dirA := a.close()

	b := newStoreOwnedBy(t, "eddie", eddieIdentity)
	chatB := b.pairedDirectly("pairing-b1", "aaaa0000aaaa0000aaaa0000aaaa0000", "Alice")
	b.textIn(chatB, "msg-exchanged", "see you at six", at(1))
	dirB := b.close()

	t.Setenv(envPassphrase, fixturePassphrase)
	sideA, err := openSide(dirA, "a", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sideA.close()
	sideB, err := openSide(dirB, "b", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sideB.close()

	var out capture
	res, err := runDiff(sideA, sideB, &out)
	if err == nil {
		t.Fatalf("two different people's stores were compared\n%s", out.String())
	}
	if res != nil {
		t.Fatal("a refused pair must produce no result")
	}

	if out.String() != "" {
		t.Fatalf("rows were compared before the refusal\n%s", out.String())
	}

	msg := err.Error()
	mustContain(t, msg, "two different people's devices")
	mustContain(t, msg, "alice")
	mustContain(t, msg, "eddie")

	mustContain(t, msg, "what was checked:")
	mustContain(t, msg, "the identity each device holds")
	mustContain(t, msg, "records 0 sibling(s)")
	mustContain(t, msg, "no flag to override")
	mustContain(t, msg, "not a tool for comparing")
}

func TestDiff_ALinkedPairCompares(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.linkDevice("id-a-uses-for-b")
	b.linkDevice("id-b-uses-for-a")
	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.matched != 1 {
		t.Fatalf("matched %d messages, want 1\n%s", res.messages.matched, report)
	}
	mustContain(t, report, "These two stores are two devices of one person.")
	mustContain(t, report, "1 of them on that identity")
}

func TestDiff_ALinkedPairThatHasNotSyncedStillCompares(t *testing.T) {
	a, b, chatA, _ := pairOfDevices(t)

	a.linkDeviceWithoutSession("id-a-uses-for-b")
	b.linkDeviceWithoutSession("id-b-uses-for-a")

	a.textOut(chatA, "msg-not-yet-there", "hello", events.DeliverySent, at(1))
	a.textOut(chatA, "msg-also-not-there", "still here", events.DeliverySent, at(2))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.messages.onlyA != 2 {
		t.Fatalf("messages only in a = %d, want 2 — an unsynced pair must still be compared\n%s",
			res.messages.onlyA, report)
	}

	if res.ownerA.siblingsOnMyIdentity != 0 || res.ownerB.siblingsOnMyIdentity != 0 {
		t.Fatalf("this fixture is meant to have no sibling sessions yet\n%s", report)
	}
	mustContain(t, report, "These two stores are two devices of one person.")
}

func TestDiff_RefusesAStoreWithNoIdentity(t *testing.T) {
	a := newStoreOwnedBy(t, "alice", aliceIdentity)
	dirA := a.close()
	b := newStoreOwnedBy(t, "nameless", nil)
	dirB := b.close()

	t.Setenv(envPassphrase, fixturePassphrase)
	sideA, err := openSide(dirA, "a", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sideA.close()
	sideB, err := openSide(dirB, "b", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sideB.close()

	var out capture
	if _, err := runDiff(sideA, sideB, &out); err == nil {
		t.Fatalf("a store with no identity was compared\n%s", out.String())
	} else {
		mustContain(t, err.Error(), "holds no identity key")
	}
}

func TestFind_AcrossTwoPeopleSaysSoAndMarksNothing(t *testing.T) {
	a := newStoreOwnedBy(t, "alice", aliceIdentity)
	chatA := a.pairedDirectly("pairing-a1", "eeee0000eeee0000eeee0000eeee0000", "Eddie")
	a.textOut(chatA, "msg-exchanged", "see you at six", events.DeliverySent, at(1))
	a.textOut(chatA, "msg-only-alice", "see you later", events.DeliverySent, at(2))
	dirA := a.close()

	b := newStoreOwnedBy(t, "eddie", eddieIdentity)
	chatB := b.pairedDirectly("pairing-b1", "aaaa0000aaaa0000aaaa0000aaaa0000", "Alice")
	b.textIn(chatB, "msg-exchanged", "see you at six", at(1))
	dirB := b.close()

	t.Setenv(envPassphrase, fixturePassphrase)
	sideA, err := openSide(dirA, "a", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sideA.close()
	sideB, err := openSide(dirB, "b", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sideB.close()

	res, report := runFindInto(t, []*side{sideA, sideB}, findFilter{text: "see you"}, false)

	if res.siblings {
		t.Fatalf("two different people were treated as siblings\n%s", report)
	}
	for _, h := range res.hits {
		if h.onlyHere {
			t.Fatalf("a hit was marked as one-sided across two people's stores\n%s", report)
		}
	}
	mustContain(t, report, "NOT two devices of one person")
	mustContain(t, report, "share the message ids")
	mustNotContain(t, report, "only in a")
}

func TestDiff_SharedIdentityWithNoSiblingRecordsStillCompares(t *testing.T) {
	a, b, chatA, chatB := pairOfDevices(t)
	a.textOut(chatA, "msg-shared", "hello", events.DeliverySent, at(1))
	b.textOut(chatB, "msg-shared", "hello", events.DeliverySent, at(1))
	dirA, dirB := a.close(), b.close()

	sideA, sideB := openSides(t, dirA, dirB)
	res, report := runDiffInto(t, sideA, sideB)

	if res.ownerA.siblings != 0 || res.ownerB.siblings != 0 {
		t.Fatalf("this fixture is meant to record no siblings\n%s", report)
	}
	if res.messages.matched != 1 {
		t.Fatalf("matched %d messages, want 1 — the identity settles it\n%s",
			res.messages.matched, report)
	}
	mustContain(t, report, "One of them records no siblings")
	mustContain(t, report, "the sibling set is written at link time and is not needed here")
}

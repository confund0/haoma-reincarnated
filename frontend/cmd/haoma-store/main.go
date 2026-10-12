package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "diff":
		os.Exit(mainDiff(os.Args[2:]))
	case "find":
		os.Exit(mainFind(os.Args[2:]))
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "no such command: %s\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `haoma-store reads a device's store. It never writes.

  haoma-store diff -a <store> -b <store>
      Say what one device holds that the other does not, and where the
      two hold the same thing differently. Two devices of ONE person.

  haoma-store find -a <store> [-b <store>] <filters>
      Find the rows that match, and print each one's full message id so
      the log tool can take it next.

The diff compares two devices of one person. It is not a tool for
comparing two people, and it refuses a pair that is not one person's
devices, by name and with no flag to override. Two people's stores share
the message ids of an exchange between them, so the comparison would
succeed and report a correct exchange as divergence.

Both commands refuse a store whose daemon is running. Stop the daemons
first, in the order the client, then haoma, then haomad.

A store directory holds meta.json and badger/. An account directory
holds frontend/. Either may be given. A relative path is looked for
below the directory you are standing in, then below the top of the tree,
then below tmp/test, which is where a manual run leaves its accounts.

The passphrase is never asked for. Set HAOMA_FRONTEND_PASSPHRASE, or
HAOMA_FRONTEND_PASSPHRASE_A and HAOMA_FRONTEND_PASSPHRASE_B when the two
stores differ, or point -vault-a and -vault-b at the accounts' vault.enc
with HAOMA_VAULT_PASSPHRASE set.

Run each command with -h for its own flags.
`)
}

const (
	exitAgreed = 0

	exitDiffers = 1

	exitRefused = 2

	exitIncomplete = 3

	exitReview = 4
)

func mainDiff(args []string) int {
	fs := flag.NewFlagSet("haoma-store diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		aPath  = fs.String("a", "", "the first store: an account directory, or the store directory inside it")
		bPath  = fs.String("b", "", "the second store, the other device of the same person")
		aVault = fs.String("vault-a", "", "path to the first account's vault.enc, when its passphrase lives there")
		bVault = fs.String("vault-b", "", "path to the second account's vault.enc")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `haoma-store diff -a <store> -b <store>

Say what one device holds that the other does not, and where the two
hold the same thing differently.

It compares conversations, messages, the delivery state and read state
on each message, file rows and their transfer state, and the contacts
set. It prints what it did not compare, how many rows matched, and what
could not be compared at all, so a run that measured nothing cannot read
as a run that found nothing.

Before anything is read it checks that the two stores are two devices of
one person, and refuses them if not. Linking works by the joining device
adopting the other device's identity key, so one person's devices hold
one identity and two people never do.

A conversation is joined by asking the contacts set which person its
pairings address, and failing that by its chat id. No one field names
the person on both devices: the device that paired directly holds only
the pairing, and the device that received the conversation from a
sibling holds the person because it has no pairing yet.

The linked-device set is read out, not compared. Each device addresses
its siblings by ids it minted itself, so the two sets never match.

Findings come in three kinds, and they mean different things:

  fields that must agree      a difference is a real finding.
  clock-stamped fields        each device stamps its own clock. A gap
                              inside the tolerance is ignored and said
                              so; outside it, it is named as drift.
  rows on one side only       may be a fault, may be what the run did.
                              Reported, never judged.

Exit codes:
  0  nothing differs at all, beyond clock gaps inside the tolerance.
  1  a field that must agree differs. The stores have not converged.
  2  refused: the two stores are not one person's devices, a daemon
     holds one, or the arguments were wrong. Nothing was read.
  3  nothing that must agree differs, but something could not be
     compared at all. Unanswered.
  4  nothing that must agree differs, but there are rows on one side
     only, or clock skew outside the tolerance. Worth reading. Not
     divergence.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitAgreed
		}
		return exitRefused
	}
	if *aPath == "" || *bPath == "" {
		fmt.Fprintln(os.Stderr, "give both stores: -a and -b")
		return exitRefused
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}

	a, err := openSide(*aPath, "a", *aVault, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}
	defer a.close()
	b, err := openSide(*bPath, "b", *bVault, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}
	defer b.close()

	if a.dataDir == b.dataDir {
		fmt.Fprintf(os.Stderr, "-a and -b name the same store, %s\n", a.dataDir)
		return exitRefused
	}

	res, err := runDiff(a, b, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}
	return verdictExit(res)
}

func mainFind(args []string) int {
	fs := flag.NewFlagSet("haoma-store find", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		aPath  = fs.String("a", "", "the store to search: an account directory, or the store directory inside it")
		bPath  = fs.String("b", "", "a second store to search as well, when you want both")
		aVault = fs.String("vault-a", "", "path to the first account's vault.enc, when its passphrase lives there")
		bVault = fs.String("vault-b", "", "path to the second account's vault.enc")

		msgID    = fs.String("msg", "", "the message id, in full or in the shortened form the logs use")
		text     = fs.String("text", "", "some of the words in the message")
		file     = fs.String("file", "", "some of an attachment's file name, or of its caption")
		conv     = fs.String("chat", "", "the conversation: a contact's name, a fingerprint, or a chat id")
		delivery = fs.String("delivery", "", "the delivery state, for example sent or delivered or read or failed")
		read     = fs.String("read", "", "yes for rows that have been read, no for rows that have not")

		at     = fs.String("at", "", "roughly when, for example 15:00 or 2026-08-15T15:00 or 2026-08-15 for the whole day")
		within = fs.String("within", "", "how far either side of -at to look, for example 30m or 2h")
		from   = fs.String("from", "", "the start of a window")
		to     = fs.String("to", "", "the end of a window")

		idsOnly = fs.Bool("ids", false, "print nothing but the matching message ids, one per line, each in full")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `haoma-store find -a <store> [-b <store>] <filters>

Find the rows that match. One store is the normal case; give -b as well
and every hit says which store it came from, and says so when the other
store does not hold that message at all.

Filters combine. All of them have to match.

This prints the text of your messages. There is no way to hide it, and
nothing is written to a file.

Each hit starts with its message id in full. That is the form to hand to
frontend/cmd/haoma-logs, which then shows everything that happened to
that message on every device. Use -ids to print nothing else.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitAgreed
		}
		return exitRefused
	}
	if *aPath == "" {
		fmt.Fprintln(os.Stderr, "give a store with -a")
		return exitRefused
	}
	if *read != "" && *read != "yes" && *read != "no" {
		fmt.Fprintf(os.Stderr, "-read takes yes or no, not %q\n", *read)
		return exitRefused
	}

	when, err := buildWindow(*at, *within, *from, *to, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}

	a, err := openSide(*aPath, "a", *aVault, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}
	defer a.close()
	sides := []*side{a}

	if *bPath != "" {
		b, err := openSide(*bPath, "b", *bVault, cwd)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitRefused
		}
		defer b.close()
		if a.dataDir == b.dataDir {
			fmt.Fprintf(os.Stderr, "-a and -b name the same store, %s\n", a.dataDir)
			return exitRefused
		}
		sides = append(sides, b)
	}

	filter := findFilter{
		msgID:    *msgID,
		text:     *text,
		file:     *file,
		conv:     *conv,
		delivery: *delivery,
		read:     *read,
		when:     when,
	}

	if _, err := runFind(sides, filter, *idsOnly, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitRefused
	}
	return exitAgreed
}

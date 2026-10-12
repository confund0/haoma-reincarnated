package main

import (
	"fmt"
	"io"
)

type ownership struct {
	name string

	identity string

	siblings int

	siblingsOnMyIdentity int
}

func ownershipOf(idx *sideIndex) ownership {
	return ownership{
		name:                 idx.side.name,
		identity:             idx.identity,
		siblings:             len(idx.devices),
		siblingsOnMyIdentity: idx.siblingsOnMyIdentity,
	}
}

func sameOwner(a, b ownership) bool {
	return a.identity != "" && a.identity == b.identity
}

func checkSameOwner(a, b ownership) error {
	switch {
	case a.identity == "" && b.identity == "":
		return fmt.Errorf(
			"refusing to compare %s and %s: neither store holds an identity key\n%s",
			a.name, b.name, evidence(a, b))
	case a.identity == "":
		return fmt.Errorf(
			"refusing to compare %s and %s: %s holds no identity key, so there is nothing to check it against\n%s",
			a.name, b.name, a.name, evidence(a, b))
	case b.identity == "":
		return fmt.Errorf(
			"refusing to compare %s and %s: %s holds no identity key, so there is nothing to check it against\n%s",
			a.name, b.name, b.name, evidence(a, b))
	case !sameOwner(a, b):
		return fmt.Errorf(
			"refusing to compare %s and %s: these are two different people's devices, not two devices of one person\n%s",
			a.name, b.name, evidence(a, b))
	}
	return nil
}

func evidence(a, b ownership) string {
	out := "\nwhat was checked:\n" +
		"  the identity each device holds. Linking is the only way a second device\n" +
		"  joins an identity, and it works by the joining device adopting the other\n" +
		"  device's identity key. Two devices of one person hold the same one.\n"
	out += fmt.Sprintf("    a  %-24s identity %s\n", a.name, identityOrNone(a.identity))
	out += fmt.Sprintf("    b  %-24s identity %s\n", b.name, identityOrNone(b.identity))
	out += "\n" +
		"  the siblings each device records. This corroborates and never decides:\n" +
		"  each device mints its own id for a sibling, so the two sets hold\n" +
		"  different ids for the same link and cannot be matched against each other.\n"
	out += fmt.Sprintf("    a  %-24s records %d sibling(s), %d of them on its own identity\n",
		a.name, a.siblings, a.siblingsOnMyIdentity)
	out += fmt.Sprintf("    b  %-24s records %d sibling(s), %d of them on its own identity\n",
		b.name, b.siblings, b.siblingsOnMyIdentity)
	out += "\n" +
		"This compares two devices of one person. It is not a tool for comparing\n" +
		"two people: their stores share message ids, so the comparison would\n" +
		"succeed and report a correct exchange as divergence.\n" +
		"There is no flag to override this."
	return out
}

func identityOrNone(fp string) string {
	if fp == "" {
		return "none held"
	}
	return shortID(fp)
}

func renderOwnership(out io.Writer, a, b ownership) {
	fmt.Fprintln(out, "These two stores are two devices of one person.")
	fmt.Fprintf(out, "  both hold identity %s\n", shortID(a.identity))
	fmt.Fprintf(out, "  a records %d sibling(s), %d of them on that identity\n",
		a.siblings, a.siblingsOnMyIdentity)
	fmt.Fprintf(out, "  b records %d sibling(s), %d of them on that identity\n",
		b.siblings, b.siblingsOnMyIdentity)
	if a.siblings == 0 || b.siblings == 0 {
		fmt.Fprintln(out, "  One of them records no siblings. The identity is what settles it;")
		fmt.Fprintln(out, "  the sibling set is written at link time and is not needed here.")
	}
	fmt.Fprintln(out)
}

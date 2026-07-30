package main

import (
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/bip39"
)

// backupScreen: the words plate on screen, with the engraver one
// keystroke away. Rendering to the terminal is inherently TTY-only,
// so the subcommand's pipe rule has nothing to refuse here.

type backupScreen struct {
	app  *tuiApp
	m    bip39.Mnemonic
	pane *pane
}

func newBackupScreen(a *tuiApp) *backupScreen {
	return &backupScreen{app: a, m: mnemonicFromKey(a.priv), pane: &pane{app: a}}
}

func (b *backupScreen) title() string { return "backup" }
func (b *backupScreen) footer() string {
	return "w engrave words · i engrave instructions · f save words file · esc back"
}

func (b *backupScreen) render(w, h int) []string {
	a, u := b.app, b.app.u
	out := []string{""}
	out = append(out, "  "+u.bold(a.keyPath)+"  ->  24 words", "")
	out = append(out, wordsGridLines(u, b.m)...)
	fp := fingerprintHex(a.priv)
	out = append(out, "",
		"  fingerprint  "+u.bold(fp[:16])+u.dim(fp[16:]),
		"  "+u.dim("plate 2 carries the bold prefix; it is public and proves a restore"),
		"",
		"  "+u.warn("these words ARE the private key: no passphrase, no BIP32, not a wallet seed"))
	if lines := b.pane.tail(max(3, h-len(out)-1)); len(lines) > 0 {
		out = append(out, "")
		for _, l := range lines {
			out = append(out, "  "+l)
		}
	}
	return out
}

func (b *backupScreen) handle(ev keyEvent) navAction {
	a := b.app
	switch {
	case ev.kind == keyEsc:
		return navPop{}
	case ev.kind == keyChar && ev.ch == 'w':
		u := a.paneUI(b.pane)
		u.printf("sending the words; the device offers the seed-plate flow (waits up to 30s)\n")
		if err := sendNFC(u, nfcRaw, []byte(b.m.String()+"\n")); err != nil {
			u.printf("%s\n", u.bad(firstLine(err.Error())))
		} else {
			u.printf("%s words delivered; follow the flow on the device\n", u.tick())
		}
	case ev.kind == keyChar && ev.ch == 'i':
		u := a.paneUI(b.pane)
		u.printf("sending the instructions plate (engraves at 5mm; waits up to 30s)\n")
		if err := sendNFC(u, nfcPlate, []byte(instructionsText(fingerprintHex(a.priv)))); err != nil {
			u.printf("%s\n", u.bad(firstLine(err.Error())))
		} else {
			u.printf("%s instructions delivered\n", u.tick())
		}
	case ev.kind == keyChar && ev.ch == 'f':
		path, ok := a.modalInput("save words", []string{
			"Write the 24 words to a file (0600, never overwrites).",
		}, "file:", "bootkey-words.txt")
		if !ok || path == "" {
			return nil
		}
		u := a.paneUI(b.pane)
		if err := writeSecretFile(path, []byte(b.m.String()+"\n")); err != nil {
			u.printf("%s\n", u.bad(err.Error()))
		} else {
			u.printf("%s wrote %s\n", u.tick(), path)
		}
	}
	return nil
}

// nsecScreen: the same scalar as a Nostr identity.

type nsecScreen struct {
	app  *tuiApp
	pane *pane
}

func newNsecScreen(a *tuiApp) *nsecScreen {
	return &nsecScreen{app: a, pane: &pane{app: a}}
}

func (n *nsecScreen) title() string  { return "nostr" }
func (n *nsecScreen) footer() string { return "e engrave nsec · esc back" }

func (n *nsecScreen) keys() (sec, pub string) {
	k, p, err := nostrKeys(n.app.priv)
	if err != nil {
		return "", ""
	}
	return k.Bech32(), p.Bech32()
}

func (n *nsecScreen) render(w, h int) []string {
	u := n.app.u
	sec, pub := n.keys()
	out := []string{"",
		"  nsec  " + u.bold(sec),
		"  npub  " + pub,
		"",
		"  " + u.warn("one secret, two protocols: whoever holds this nsec can sign firmware"),
		"  " + u.warn("for the fused boards, and the boot key words plate restores this nsec"),
	}
	if lines := n.pane.tail(max(3, h-len(out)-1)); len(lines) > 0 {
		out = append(out, "")
		for _, l := range lines {
			out = append(out, "  "+l)
		}
	}
	return out
}

func (n *nsecScreen) handle(ev keyEvent) navAction {
	switch {
	case ev.kind == keyEsc:
		return navPop{}
	case ev.kind == keyChar && ev.ch == 'e':
		u := n.app.paneUI(n.pane)
		sec, _ := n.keys()
		u.printf("sending the nsec; the device derives the npub and offers both plates\n")
		if err := sendNFC(u, nfcRaw, []byte(sec+"\n")); err != nil {
			u.printf("%s\n", u.bad(firstLine(err.Error())))
		} else {
			u.printf("%s nsec delivered\n", u.tick())
		}
	}
	return nil
}

// restoreScreen embeds the word-entry engine, resolves unknowns and
// mis-reads against a fingerprint, and saves under the never-clobber
// rules. With a local key present it doubles as plate verification.

type restoreScreen struct {
	app   *tuiApp
	entry *interactiveEntry
	done  bool
	fail  error
	priv  *secp256k1.PrivateKey
	notes []string
	pane  *pane
}

func newRestoreScreen(a *tuiApp) *restoreScreen {
	return &restoreScreen{
		app:   a,
		entry: &interactiveEntry{tty: a.tty, u: a.u},
		pane:  &pane{app: a},
	}
}

func (r *restoreScreen) title() string { return "restore" }

func (r *restoreScreen) footer() string {
	if !r.done {
		return "type words · backspace fix · ? unknown word · esc cancel"
	}
	if r.priv != nil {
		return "s save PEM · esc done"
	}
	return "esc back"
}

func (r *restoreScreen) render(w, h int) []string {
	var out []string
	if !r.done {
		block := strings.Split(strings.TrimSuffix(r.entry.render(), "\n"), "\n")
		return append(out, block...)
	}
	u := r.app.u
	out = append(out, "")
	if len(r.entry.entries) == 24 {
		m := make(bip39.Mnemonic, 0, 24)
		allKnown := true
		for _, en := range r.entry.entries {
			if en.unknown {
				allKnown = false
				break
			}
			m = append(m, en.w)
		}
		if allKnown && r.priv != nil {
			out = append(out, wordsGridLines(u, m)...)
			out = append(out, "")
		}
	}
	for _, n := range r.notes {
		out = append(out, "  "+n)
	}
	if r.fail != nil {
		for _, l := range strings.Split(r.fail.Error(), "\n") {
			out = append(out, "  "+u.bad(l))
		}
	}
	if lines := r.pane.tail(max(3, h-len(out)-1)); len(lines) > 0 {
		out = append(out, "")
		for _, l := range lines {
			out = append(out, "  "+l)
		}
	}
	return out
}

func (r *restoreScreen) handle(ev keyEvent) navAction {
	if r.done {
		switch {
		case ev.kind == keyEsc:
			return navPop{}
		case ev.kind == keyChar && ev.ch == 's' && r.priv != nil:
			r.save()
		}
		return nil
	}
	switch ev.kind {
	case keyEsc:
		return navPop{}
	case keyChar:
		r.entry.key(ev.ch)
	case keyEnter:
		r.entry.key('\r')
	case keyBackspace:
		r.entry.key(0x7f)
	}
	if len(r.entry.entries) == 24 {
		r.finalize()
	}
	return nil
}

// finalize resolves the entry into a key, borrowing the subcommand's
// resolution pipeline with the fingerprint sourced interactively.
func (r *restoreScreen) finalize() {
	a := r.app
	u := a.paneUI(r.pane)
	r.done = true

	words := make([]bip39.Word, 24)
	var unknown []int
	for i, en := range r.entry.entries {
		if en.unknown {
			unknown = append(unknown, i)
			words[i] = -1
		} else {
			words[i] = en.w
		}
	}

	want, verifyLabel := r.expectedFingerprint(len(unknown) > 0 || !bip39.Mnemonic(words).Valid())
	m, err := resolveMnemonic(u, words, unknown, want, want != nil, false)
	if err != nil && want != nil {
		// One more rung: the pair search, offered, never silent.
		if _, ok := a.modalInput("repair", []string{
			firstLine(err.Error()),
			"Search every two-word substitution too? This takes minutes.",
		}, "enter to search, esc to stop", ""); ok {
			m, err = resolveMnemonic(u, words, unknown, want, false, true)
		}
	}
	if err != nil {
		r.fail = err
		return
	}
	priv, err := keyFromMnemonic(m)
	if err != nil {
		r.fail = err
		return
	}
	r.priv = priv
	for i, w := range m {
		r.entry.entries[i] = wordEntry{w: w}
	}
	fp := fingerprintHex(priv)
	r.notes = append(r.notes, "fingerprint  "+a.u.bold(fp[:16])+a.u.dim(fp[16:]))
	if want != nil {
		if fingerprintMatches(want, fingerprint(priv)) {
			r.notes = append(r.notes, a.u.good("matches "+verifyLabel+" ")+a.u.tick())
		} else {
			r.fail = fmt.Errorf("fingerprint mismatch against %s; nothing will be written", verifyLabel)
			r.priv = nil
			return
		}
	}
	if a.priv != nil {
		if fingerprint(a.priv) == fingerprint(priv) {
			r.notes = append(r.notes, a.u.good("byte-identical to "+a.keyPath+" ")+a.u.tick())
		} else {
			r.notes = append(r.notes, a.u.warn("differs from "+a.keyPath+": this is some other key"))
		}
	}
	if a.board != nil {
		if s := slotOfKey(a.board, fingerprint(priv)); s >= 0 {
			r.notes = append(r.notes, a.u.good(fmt.Sprintf("fused and valid in slot %d of the attached board ", s))+a.u.tick())
		}
	}
}

// expectedFingerprint picks the verification target: the local key
// when present, else - when resolution needs one - the operator's
// typed value, with the attached board's slot hashes offered as the
// crib they are fused to be.
func (r *restoreScreen) expectedFingerprint(required bool) ([]byte, string) {
	a := r.app
	if a.priv != nil {
		fp := fingerprint(a.priv)
		return fp[:], a.keyPath
	}
	if !required {
		return nil, ""
	}
	intro := []string{"Recovering needs the public key fingerprint from plate 2."}
	if a.board != nil {
		intro = append(intro, "The attached board's fused slots:")
		for i := range a.board.slots {
			s := &a.board.slots[i]
			if s.readable && !s.zero {
				intro = append(intro, fmt.Sprintf("  slot %d: %s", i, hexOf(s.hash)[:32]+"…"))
			}
		}
	}
	for {
		text, ok := a.modalInput("fingerprint", intro, "fingerprint (16+ hex):", "")
		if !ok {
			return nil, ""
		}
		want, err := parseFingerprint(text)
		if err == nil {
			return want, "the entered fingerprint"
		}
		intro = append(intro[:1], err.Error())
	}
}

func (r *restoreScreen) save() {
	a := r.app
	def := defaultKeyPath
	if a.keyPath != "" {
		def = a.keyPath
	}
	path, ok := a.modalInput("save", []string{
		"Write the PEM (0600). An existing different key is never overwritten;",
		"an identical one may be rewritten.",
	}, "file:", def)
	if !ok || path == "" {
		return
	}
	u := a.paneUI(r.pane)
	pemBytes := marshalKeyPEM(r.priv)
	err := writeKeyPEMFile(path, pemBytes, false, fingerprint(r.priv))
	if err != nil {
		if old, perr := loadKeyFile(path); perr == nil && fingerprint(old) == fingerprint(r.priv) {
			if _, ok := a.modalInput("overwrite", []string{
				path + " already holds this same key.",
			}, "enter to rewrite it, esc to keep it", ""); ok {
				err = writeKeyPEMFile(path, pemBytes, true, fingerprint(r.priv))
			} else {
				return
			}
		}
	}
	if err != nil {
		u.printf("%s\n", u.bad(err.Error()))
		return
	}
	u.printf("%s wrote %s\n", u.tick(), path)
	a.reloadKey()
}

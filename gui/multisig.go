package gui

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
)

// Bounds for the cosigner count. Two is the smallest quorum that is
// one; seven matches the largest descriptor a plate has carried on
// the bench, and the plate fit still gates every layout downstream.
const (
	minCosigners = 2
	maxCosigners = 7
)

var (
	errKeyNoOrigin = errors.New("The key carries no master fingerprint. Export it with key origin information, or wallets cannot match it to its signer.")
	errKeyNetwork  = errors.New("The key is not for mainnet.")
)

// The recovery-complexity warning, shown per cosigner before the
// passphrase editor opens. The single-seed flow does not carry it:
// one passphrase is a choice, N of them multiply the secrets a
// recovery needs, and the person most likely to add them all is the
// one setting up every cosigner in a row right now.
const multisigPassphraseWarning = "A passphrase changes this cosigner's key. " +
	"The wallet only opens again if the passphrase is typed at recovery: " +
	"the seed plate alone no longer names it, and the passphrase plate " +
	"must live apart from the seed plate.\n\n" +
	"A passphrase has no checksum, and it is one more secret per cosigner."

// cosignerSlot holds one cosigner while the wallet assembles: the
// account key always, the seed and passphrase only when they were
// entrusted to this machine, for the plates after the wallet exists.
type cosignerSlot struct {
	key        bip380.Key
	mnemonic   bip39.Mnemonic
	passphrase string
	// source names where the key came from on the confirm screen.
	source string
}

// multisigWalletFlow builds an M-of-N P2WSH wallet on the machine:
// a required title, the quorum, then one cosigner at a time — words
// typed on the keyboard, a seed tapped over NFC, or a public key
// tapped by a cosigner who keeps their seed to themselves. The
// assembled descriptor then runs the ordinary descriptor machinery
// for its plates, preceded by each entrusted seed's own plates.
// It reports whether the operator saw the flow through to its end.
func multisigWalletFlow(ctx *Context, th *Colors) (done bool) {
	slots := make([]cosignerSlot, 0, maxCosigners)
	// The seeds live only for the length of the flow. Go strings are
	// immutable, so the passphrases cannot be scrubbed the same way;
	// they get collected like the single-seed flow's.
	defer func() {
		for _, s := range slots {
			for i := range s.mnemonic {
				s.mnemonic[i] = -1
			}
		}
	}()
	title, ok := titleFlow(ctx, th, true, "")
	if !ok {
		return false
	}
	n, ok := cosignerCountFlow(ctx, th)
	if !ok {
		return false
	}
	m, ok := thresholdFlow(ctx, th, n)
	if !ok {
		return false
	}
	for len(slots) < n {
		slot, ok := cosignerFlow(ctx, th, slots, len(slots), n)
		if !ok {
			if len(slots) == 0 {
				return false
			}
			if confirmDiscardWallet(ctx, th) {
				return false
			}
			continue
		}
		slots = append(slots, slot)
	}
	desc := &bip380.Descriptor{
		Title:     title,
		Script:    bip380.P2WSH,
		Type:      bip380.SortedMulti,
		Threshold: m,
	}
	for _, s := range slots {
		desc.Keys = append(desc.Keys, s.key)
	}
	for {
		if !reviewWalletFlow(ctx, th, desc) {
			if confirmDiscardWallet(ctx, th) {
				return false
			}
			continue
		}
		// The coordinator scans the wallet and both ends compare the
		// first address before anything is cut: an entry mistake
		// caught here costs minutes, not steel.
		if !exportDescriptorFlow(ctx, th, desc) {
			continue
		}
		break
	}
	// The plates: each entrusted seed first, so a cosigner's seed
	// plate and passphrase plate leave the bench together, then the
	// descriptor through the machinery every scanned multisig runs —
	// one plate, a split, or full copies.
	pathStr := bip380.P2WSH.DerivationPath().String()
	for k := range slots {
		s := &slots[k]
		if s.mnemonic == nil {
			continue
		}
		ss := &SeedScreen{Title: fmt.Sprintf("Cosigner %d of %d", k+1, n)}
		lead := fmt.Sprintf("Cosigner %d of %d, %.8X", k+1, n, s.key.MasterFingerprint)
		for !seedPlateFlow(ctx, th, ss, s.mnemonic, s.passphrase, desc.Title, lead) {
		}
		passphrasePlateFlow(ctx, th, s.mnemonic, s.passphrase, pathStr, desc.Title)
	}
	descriptorFlow(ctx, th, desc)
	return true
}

// cosignerCountFlow picks N. The list opens on three, the smallest
// count anyone spreads across sites.
func cosignerCountFlow(ctx *Context, th *Colors) (int, bool) {
	choices := make([]string, 0, maxCosigners-minCosigners+1)
	for i := minCosigners; i <= maxCosigners; i++ {
		choices = append(choices, fmt.Sprintf("%d COSIGNERS", i))
	}
	cs := &ChoiceScreen{
		Title:   "Cosigners",
		Lead:    "How many keys share the wallet?",
		Choices: choices,
	}
	cs.choice = 1 // 3 cosigners
	choice, ok := cs.Choose(ctx, th)
	if !ok {
		return 0, false
	}
	return minCosigners + choice, true
}

// thresholdFlow picks M, opening on two: 2-of-3 is the shape most
// setups take, and 2-of-2 is the right default answer for a pair.
func thresholdFlow(ctx *Context, th *Colors, n int) (int, bool) {
	choices := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		choices = append(choices, fmt.Sprintf("%d OF %d", i, n))
	}
	cs := &ChoiceScreen{
		Title:   "Threshold",
		Lead:    "How many must sign to spend?",
		Choices: choices,
	}
	cs.choice = 1
	choice, ok := cs.Choose(ctx, th)
	if !ok {
		return 0, false
	}
	return choice + 1, true
}

// cosignerEntryAction is what the landing page produced: a word
// count to type, or a tapped payload — a seed, or a cosigner key.
type cosignerEntryAction struct {
	words int
	scan  any
}

// rejectHold is how long a refused tap's line stays up: long enough to
// read the reason it names, where scanStatusTimeout only has to cover
// the decay of a transient status word.
const rejectHold = 3 * time.Second

// cosignerEntry is the cosigner's landing page: the word-count rows
// for typing, with the scanner already listening — the start screen's
// pattern pointed at one step. A tap dispatches by what arrived: a
// seed or a cosigner key returns immediately, anything else shows the
// reject line and keeps listening. Back reports false.
func cosignerEntry(ctx *Context, th *Colors, title string) (cosignerEntryAction, bool) {
	scans := make(chan scanResult, 1)
	stop := scanWorker(ctx, scans)
	defer stop()
	cs := &ChoiceScreen{
		Title:   title,
		Lead:    "Enter the seed words, or tap a seed or cosigner key",
		Choices: []string{"12 WORDS", "24 WORDS"},
	}
	inp := new(InputTracker)
	cancelBtn := &Clickable{Button: Button1}
	chooseBtn := &Clickable{Button: Button3, AltButton: Center}
	var status scanStatus
	var detail string
	var rejected bool
	var rejectWhy string
	var statusTimeout time.Time
	// A refusal has a reason to read, so it holds the line past the
	// scan status decay and is not shortened by the status updates
	// that keep arriving while the writer sits in the field.
	var rejectUntil time.Time
	for !ctx.Done {
		switch {
		case cancelBtn.Clicked(ctx):
			return cosignerEntryAction{}, false
		case chooseBtn.Clicked(ctx):
			return cosignerEntryAction{words: []int{12, 24}[cs.choice]}, true
		}
		for i := range cs.children {
			c := &cs.children[i]
			if c.click.Clicked(ctx) {
				cs.choice = i
			}
		}
		for {
			e, ok := inp.Next(ctx, ButtonFilter(Up), ButtonFilter(Down))
			if !ok {
				break
			}
			if e, ok := e.AsButton(); ok && e.Pressed {
				switch e.Button {
				case Up:
					if cs.choice > 0 {
						cs.choice--
					}
				case Down:
					if cs.choice < len(cs.Choices)-1 {
						cs.choice++
					}
				}
			}
		}
		select {
		case scan := <-scans:
			now := time.Now()
			if now.Before(statusTimeout) {
				status = max(status, scan.Status)
			} else {
				status = scan.Status
			}
			detail = scan.Detail
			statusTimeout = now.Add(scanStatusTimeout)
			if obj := scan.Object; obj != nil {
				rejected, rejectWhy = false, ""
				switch obj := obj.(type) {
				case debugCommand:
					// The provisioning channel belongs to the start
					// screen, not to a flow step.
				case bip39.Mnemonic:
					return cosignerEntryAction{scan: obj}, true
				default:
					key, why, ok := cosignerFromPayloadReason(obj)
					if ok {
						return cosignerEntryAction{scan: key}, true
					}
					rejected, rejectWhy = true, why
					rejectUntil = now.Add(rejectHold)
				}
			}
		default:
		}
		dims := ctx.Platform.DisplaySize()
		sttxt := ""
		now := time.Now()
		if rejected && now.Before(rejectUntil) {
			ctx.WakeupAt(rejectUntil)
			if rejectWhy != "" {
				sttxt = "Not a cosigner key: " + rejectWhy
			} else {
				sttxt = "Not a seed or cosigner key"
			}
		} else if now.Before(statusTimeout) {
			ctx.WakeupAt(statusTimeout)
			sttxt = scanStatusText(status)
			if status == scanParts && detail != "" {
				sttxt = detail
			}
		}
		subt, ssz := widget.Labelw(&ctx.B, ctx.Styles.subtitle, 300, th.Text, sttxt)
		r := layout.Rectangle{Max: dims}
		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: cancelBtn, Style: StyleSecondary, Icon: assets.IconBack},
			NavButton{Clickable: chooseBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		)
		ctx.Frame(op.Layer(
			// Above the lead's band, which the status would otherwise
			// share pixels with.
			subt.Offset(r.S(ssz).Sub(image.Pt(0, leadingSize))),
			nav,
			cs.Draw(ctx, th, dims),
		))
	}
	return cosignerEntryAction{}, false
}

// cosignerFlow collects cosigner k: a key, however it arrives, past
// the duplicate check and a confirm. Backing out of the landing page
// reports false; the caller decides what abandoning means.
func cosignerFlow(ctx *Context, th *Colors, slots []cosignerSlot, k, n int) (cosignerSlot, bool) {
	title := fmt.Sprintf("Cosigner %d of %d", k+1, n)
	for {
		act, ok := cosignerEntry(ctx, th, title)
		if !ok {
			return cosignerSlot{}, false
		}
		var slot cosignerSlot
		switch scan := act.scan.(type) {
		case nil:
			mnemonic := emptyBIP39Mnemonic(act.words)
			inputWordsFlow(ctx, th, mnemonic, 0)
			if isEmptyMnemonic(mnemonic) {
				continue
			}
			slot, ok = seedCosigner(ctx, th, title, mnemonic)
		case bip39.Mnemonic:
			slot, ok = seedCosigner(ctx, th, title, scan)
		case bip380.Key:
			slot, ok = keyCosigner(ctx, th, scan)
		}
		if !ok {
			continue
		}
		if dup := slotWithFingerprint(slots, slot.key.MasterFingerprint); dup >= 0 {
			// A doubled fingerprint is a doubled signer: the same seed
			// tapped twice, not a drawn coincidence. Words inside one
			// seed may repeat; whole cosigners may not.
			showError(ctx, th, fmt.Errorf("Already cosigner %d: the same master fingerprint %.8X.", dup+1, slot.key.MasterFingerprint), blankScreen)
			continue
		}
		if confirmCosigner(ctx, th, slot, k, n) {
			return slot, true
		}
	}
}

// seedCosigner takes an entrusted seed the rest of the way to a slot:
// the same review and validity gate a single seed passes (checksum,
// the Electrum rejection, reading the words back against the tiles or
// the tag's owner), the passphrase ask behind its warning, then the
// account key derivation.
func seedCosigner(ctx *Context, th *Colors, title string, mnemonic bip39.Mnemonic) (cosignerSlot, bool) {
	ss := &SeedScreen{Title: title}
	if !ss.Confirm(ctx, th, mnemonic) {
		return cosignerSlot{}, false
	}
	pass, ok := multisigPassphraseFlow(ctx, th, mnemonic)
	if !ok {
		return cosignerSlot{}, false
	}
	// Seconds of PBKDF2 with a passphrase; behind the progress screen
	// like the seed plate's fingerprint derivation.
	key, err := runJob(ctx, th, func(pump func(done, total int) bool) (bip380.Key, error) {
		return cosignerKey(mnemonic, pass)
	}, planFrame(ctx, th, blankScreen))
	if err != nil {
		if !errors.Is(err, errPlanCanceled) {
			showError(ctx, th, err, blankScreen)
		}
		return cosignerSlot{}, false
	}
	source := "Seed on this machine"
	if pass != "" {
		source = "Seed with passphrase"
	}
	return cosignerSlot{key: key, mnemonic: mnemonic, passphrase: pass, source: source}, true
}

// keyCosigner vets a tapped account key: the cosigner who never shows
// this machine a seed. The origin is not optional — without the
// fingerprint no wallet can match the key to its signer, and the
// duplicate check downstream would be blind.
func keyCosigner(ctx *Context, th *Colors, key bip380.Key) (cosignerSlot, bool) {
	if key.MasterFingerprint == 0 {
		showError(ctx, th, errKeyNoOrigin, blankScreen)
		return cosignerSlot{}, false
	}
	if key.Network != &chaincfg.MainNetParams {
		showError(ctx, th, errKeyNetwork, blankScreen)
		return cosignerSlot{}, false
	}
	if len(key.Children) == 0 {
		// The tapped expression named no receive/change branches; the
		// descriptor needs them spelled out (see seedDescriptor).
		key.Children = []bip380.Derivation{
			{Type: bip380.RangeDerivation, Index: 0, End: 1},
			{Type: bip380.WildcardDerivation},
		}
	}
	return cosignerSlot{key: key, source: "Public key only"}, true
}

// cosignerKey derives the BIP48 P2WSH account key a seed contributes
// to a multisig: the same recipe as the single-sig descriptor, at the
// multisig path, receive and change branches in the key.
func cosignerKey(m bip39.Mnemonic, passphrase string) (bip380.Key, error) {
	network := &chaincfg.MainNetParams
	mk, ok := deriveMasterKey(m, passphrase, network)
	if !ok {
		return bip380.Key{}, errDescriptorDerive
	}
	master, err := mk.ECPubKey()
	if err != nil {
		return bip380.Key{}, err
	}
	path := bip380.P2WSH.DerivationPath()
	xpub, err := bip32.Derive(mk, path)
	if err != nil {
		return bip380.Key{}, err
	}
	account, err := xpub.ECPubKey()
	if err != nil {
		return bip380.Key{}, err
	}
	return bip380.Key{
		Network:           network,
		MasterFingerprint: bip32.Fingerprint(master),
		DerivationPath:    path,
		// In the descriptor itself, not defaulted at derivation: see
		// the note in seedDescriptor.
		Children: []bip380.Derivation{
			{Type: bip380.RangeDerivation, Index: 0, End: 1},
			{Type: bip380.WildcardDerivation},
		},
		KeyData:           account.SerializeCompressed(),
		ChainCode:         xpub.ChainCode(),
		ParentFingerprint: xpub.ParentFingerprint(),
	}, nil
}

// cosignerFromPayload extracts a cosigner key from the payloads a tap
// can decode to. A bare key expression with a multisig origin path
// falls through the scanner's descriptor promotion (which only knows
// single-sig paths) and arrives as text; parse it here instead of
// teaching the start screen a payload kind only this flow wants.
func cosignerFromPayload(obj any) (bip380.Key, bool) {
	k, _, ok := cosignerFromPayloadReason(obj)
	return k, ok
}

// cosignerFromPayloadReason is cosignerFromPayload with the reason a
// key-shaped text was refused, for the reject line. Only text that
// opens like a key expression carries one: a rejected seed phrase or
// free text is not a key, and its ParseKey error would mislead.
func cosignerFromPayloadReason(obj any) (bip380.Key, string, bool) {
	switch o := obj.(type) {
	case *bip380.Descriptor:
		if o.Type == bip380.Singlesig && len(o.Keys) == 1 {
			return o.Keys[0], "", true
		}
	case plainText:
		k, err := bip380.ParseKey(nil, []byte(o))
		if err == nil {
			return k, "", true
		}
		if keyShaped(string(o)) {
			return bip380.Key{}, reasonText(err), false
		}
	}
	return bip380.Key{}, "", false
}

// keyShaped reports whether s opens like a key expression: an origin
// in brackets, or an extended key of any of the version prefixes.
func keyShaped(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		return true
	}
	return len(s) >= 4 && strings.EqualFold(s[1:4], "pub")
}

func slotWithFingerprint(slots []cosignerSlot, mfp uint32) int {
	for i, s := range slots {
		if s.key.MasterFingerprint == mfp {
			return i
		}
	}
	return -1
}

// confirmCosigner shows what was just added before it counts: the
// fingerprint the plates and the coordinator will both display, and
// where the key came from. Back discards the slot.
func confirmCosigner(ctx *Context, th *Colors, slot cosignerSlot, k, n int) bool {
	gate := &noticeScreen{
		Title: strings.ToTitle(fmt.Sprintf("Cosigner %d of %d", k+1, n)),
		Body: fmt.Sprintf("Fingerprint %.8X\n%s\n\nConfirm to add this cosigner.",
			slot.key.MasterFingerprint, slot.source),
		Icon: assets.IconCheckmark,
	}
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, res := gate.Layout(ctx, th, dims)
		switch res {
		case ConfirmNo:
			return false
		case ConfirmYes:
			return true
		}
		ctx.Frame(d)
	}
	return false
}

// multisigPassphraseFlow is passphraseFlow with the multisig warning
// between the choice and the editor.
func multisigPassphraseFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic) (string, bool) {
	for {
		cs := &ChoiceScreen{
			Title:   "Passphrase",
			Lead:    "Add a passphrase to this seed?",
			Choices: []string{"NO PASSPHRASE", "ADD PASSPHRASE"},
		}
		choice, ok := cs.Choose(ctx, th)
		if !ok {
			return "", false
		}
		if choice == 0 {
			return "", true
		}
		gate := &noticeScreen{
			Title: strings.ToTitle("Passphrase?"),
			Body:  multisigPassphraseWarning,
			Icon:  assets.IconCheckmark,
		}
	warn:
		for !ctx.Done {
			dims := ctx.Platform.DisplaySize()
			d, res := gate.Layout(ctx, th, dims)
			switch res {
			case ConfirmNo:
				break warn
			case ConfirmYes:
				pass, ok := passphraseEditFlow(ctx, th, mnemonic)
				if ok {
					return pass, true
				}
				break warn
			}
			ctx.Frame(d)
		}
	}
}

// reviewWalletFlow is the last look before the wallet exists: the
// title, the quorum and every fingerprint, held to confirm. False
// means the operator backed out.
func reviewWalletFlow(ctx *Context, th *Colors, desc *bip380.Descriptor) bool {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%d-of-%d multisig, %s\n\n", desc.Title, desc.Threshold, len(desc.Keys), desc.Script.String())
	for i, k := range desc.Keys {
		fmt.Fprintf(&b, "%d  %.8X\n", i+1, k.MasterFingerprint)
	}
	b.WriteString("\nHold button to create the wallet.")
	scr := &ConfirmWarningScreen{
		Title: strings.ToTitle("Create Wallet?"),
		Body:  b.String(),
		Icon:  assets.IconCheckmark,
	}
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, res := scr.Layout(ctx, th, dims)
		switch res {
		case ConfirmYes:
			return true
		case ConfirmNo:
			return false
		}
		ctx.Frame(d)
	}
	return false
}

func confirmDiscardWallet(ctx *Context, th *Colors) bool {
	scr := &ConfirmWarningScreen{
		Title: strings.ToTitle("Discard Wallet?"),
		Body:  "Going back discards every cosigner entered.\n\nHold button to confirm.",
		Icon:  assets.IconDiscard,
	}
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, res := scr.Layout(ctx, th, dims)
		switch res {
		case ConfirmYes:
			return true
		case ConfirmNo:
			return false
		}
		ctx.Frame(d)
	}
	return true
}

# Set a custom boot key on your SeedHammer II

You own a SeedHammer II locked with the manufacturer's signing key, and you want to play with your own firmware on it. You can. The RP2350 chip has four boot-key slots; SeedHammer fills one. Three are open. With your own secp256k1 key, `picotool`, and the repo's `picosign` tool, you program a second slot and boot whatever you've signed.

This guide is the fun-and-games path. The manufacturer firmware is untouched; you're adding a parallel boot key alongside it. For a production-grade signing setup, run your key on an HSM end-to-end and follow the RP2350 datasheet §10.5 procedure carefully.

## Before you start

You will need:

- A SeedHammer II controller (an SH2 board, with or without the engraver attached).
- A USB-C cable to a host computer.
- A spare secp256k1 keypair in PEM format. Generate one with OpenSSL:

  ```sh
  openssl ecparam -name secp256k1 -genkey -noout -out my-key.pem
  openssl ec -in my-key.pem -pubout -out my-pubkey.pem
  ```

  Keep `my-key.pem` somewhere safe. Lose it and you can't sign with this slot again.

- [`picotool`](https://github.com/raspberrypi/picotool) version 2.0 or later. Pre-built binaries: <https://github.com/raspberrypi/pico-sdk-tools/releases>. On macOS, `brew install picotool` works.
- The SeedHammer source tree, with `nix` and the project flake usable, plus a Go toolchain for `picosign`: <https://github.com/SeedHammer/seedhammer>. The flake builds on Linux and macOS.

Confirm picotool sees your SH2:

1. Unplug the SH2.
2. Hold the **BOOTSEL** button on the board.
3. Plug USB in while holding the button. Release after about a second.
4. Run:

   ```sh
   picotool info
   ```

   You should see a `Program Information` block reporting an RP2350 with a verified ARM Secure image, and a mass-storage volume named `SHII` appears (its `INFO_UF2.TXT` reads `Model: SeedHammer II`).

   "No accessible RP-series devices in BOOTSEL mode were found" means the device isn't in BOOTSEL; redo the button dance. A device that is listed but can't be opened is a USB permission problem: install picotool's udev rules on Linux, or retry with `sudo` on macOS.

## What you are about to do

In OTP terms, on a stock SeedHammer-locked SH2:

| Slot | State | After this guide |
|---|---|---|
| `BOOTKEY0` | SeedHammer hash, `KEY_VALID` | unchanged |
| `BOOTKEY1` | empty (all zeros), writable | **your hash**, `KEY_VALID` |
| `BOOTKEY2` | empty (all zeros), writable | unchanged |
| `BOOTKEY3` | empty (all zeros), writable | unchanged |

The boot ROM accepts signatures from any valid slot. Firmware signed by SeedHammer keeps working. Firmware signed by you also works.

> **Heads-up: irreversible**
>
> Everything you write to OTP is permanent. Read every command before pressing enter. The most common bricking cause is computing the wrong hash and writing it to a slot. That slot is then poisoned forever; the bit pattern can never be cleared. The procedure below has picotool compute the hash from your PEM file, and verifies every row before the slot is marked valid. **Don't** try to compute the hash by hand unless you know exactly why the boot key hash is SHA-256 of the uncompressed 64-byte X‖Y form (not the 33-byte compressed form, not the 65-byte form with the `04` prefix). The byte layout is enforced at [`picobin/picobin.go:103`](https://github.com/seedhammer/seedhammer/blob/main/picobin/picobin.go#L103). X/Y assembly happens in [`cmd/picosign/main.go:142-143`](https://github.com/seedhammer/seedhammer/blob/main/cmd/picosign/main.go#L142-L143). The full procedure is in the [RP2350 datasheet §10.5](https://datasheets.raspberrypi.com/rp2350/rp2350-datasheet.pdf).

## Step 1: Inspect the current state

Confirm reality matches the table.

```sh
# Enter BOOTSEL: hold the button, plug USB, release after one second.
picotool otp get CRIT1.SECURE_BOOT_ENABLE
picotool otp get BOOT_FLAGS1.KEY_VALID
picotool otp get BOOT_FLAGS1.KEY_INVALID
for k in BOOTKEY0_0 BOOTKEY1_0 BOOTKEY2_0 BOOTKEY3_0; do
  echo "--- $k ---"
  picotool otp get -e "$k"
done
```

On a stock SH2 locked by SeedHammer you should see:

```
CRIT1.SECURE_BOOT_ENABLE      = 0x1
BOOT_FLAGS1.KEY_VALID         = 0x1   (slot 0 only)
BOOT_FLAGS1.KEY_INVALID       = 0x0   (no slots revoked)
BOOTKEY0_0                    = (non-zero, the SeedHammer hash)
BOOTKEY1_0, BOOTKEY2_0, BOOTKEY3_0 = 0x0000
```

If `KEY_VALID` shows a different bit, fine. [`AddBootKey`](https://github.com/seedhammer/seedhammer/blob/main/driver/otp/otp.go#L102-L157) picks a best-fit slot, so the manufacturer's slot may not be 0. Use any slot that shows `0x0000` for the steps below.

If `KEY_INVALID` already has bits set for slots 1, 2, and 3 (i.e. `0xE`), stop. Someone has revoked the secondary slots and this guide won't work.

Save the dump for your records:

```sh
picotool otp dump > otp-before-$(date -u +%Y%m%dT%H%M%SZ).txt
```

## Step 2: Generate the OTP-JSON for your key

Use `picotool seal --sign` on any binary picotool accepts — the firmware you'll build in step 4, or a `hello_world.elf` from [pico-examples](https://github.com/raspberrypi/pico-examples). The signed output is thrown away; what we want is the OTP-JSON byproduct, which carries your pubkey's correctly-hashed fingerprint:

```sh
picotool seal --sign placeholder.elf discard.elf my-key.pem my-otp.json
rm discard.elf
```

`my-otp.json` holds the 32 bytes of SHA-256 of your public key, plus two flag entries:

```json
{
    "boot_flags1": { "key_valid": 1 },
    "bootkey0": [ 180, 81, 37, ...32 bytes... ],
    "crit1": { "secure_boot_enable": 1 }
}
```

Edit it down to just your slot:

1. Rename `bootkey0` to `bootkey1` (or whichever empty slot you picked). picotool expands the array to the sixteen OTP rows at load time, two bytes per row, low byte first.
2. Delete `boot_flags1`. The valid bit is set in step 3, after the hash rows are verified: a slot marked valid while holding a faulty hash can leave the device unbootable.
3. Delete `crit1`. Secure boot is already on.

You're left with:

```json
{
    "bootkey1": [ 180, 81, 37, ...your 32 bytes... ]
}
```

## Step 3: Load it onto the device

Confirm you're still in BOOTSEL (`picotool info` shows the device), then:

```sh
picotool otp load my-otp.json
```

For JSON input picotool prints what it writes but reads nothing back, so verify the rows yourself:

```sh
picotool otp get -e BOOTKEY1_0 BOOTKEY1_1 BOOTKEY1_2 BOOTKEY1_3 \
    BOOTKEY1_4 BOOTKEY1_5 BOOTKEY1_6 BOOTKEY1_7 \
    BOOTKEY1_8 BOOTKEY1_9 BOOTKEY1_10 BOOTKEY1_11 \
    BOOTKEY1_12 BOOTKEY1_13 BOOTKEY1_14 BOOTKEY1_15
```

Row *N* holds array bytes 2*N* and 2*N*+1, low byte first: with an array starting `180, 81, 37, 136`, row 0 reads `0x51b4` and row 1 `0x8825`. All sixteen must match. If any row disagrees, **stop**: the slot is not marked valid, the device still boots official firmware, and two spare slots remain.

When all sixteen match, mark the slot valid:

```sh
picotool otp set -s BOOT_FLAGS1.KEY_VALID 0x2   # OR in the bit for slot 1; slot 2 = 0x4, slot 3 = 0x8
picotool otp get BOOT_FLAGS1.KEY_VALID          # expect 0x3 (slots 0 and 1)
```

`-s` ORs the bit into the existing field, refuses any write that would clear a bit, and writes all three redundant copies of the `BOOT_FLAGS1` row.

## Step 4: Build and sign a firmware image

In the SeedHammer repo:

```sh
nix run .#build-firmware
```

This produces `seedhammerii-<version>.uf2` with the picobin signature section present but zeroed.

Don't reach for `picotool seal --sign` here: sealing an already-sealed image appends a second image-definition block instead of filling the first, and the boot ROM rejects the result (the symptom is three metadata blocks in `picotool info -a`). Sign in place with the repo's `picosign` instead. The signature covers the embedded public key, so the key goes in first, then the digest, then the signature:

```sh
FW=seedhammerii-<version>.uf2
PUB=$(openssl ec -in my-key.pem -pubout -conv_form compressed -outform DER | tail -c 33 | xxd -p -c 33)

# 1. Embed your pubkey, signature still zeroed.
go run seedhammer.com/cmd/picosign sign -pubkey "$PUB" -sig "$(printf '0%.0s' {1..128})" "$FW"
# 2. Extract the 32-byte digest the boot ROM will verify.
go run seedhammer.com/cmd/picosign hash "$FW" > digest.bin
# 3. Sign the digest.
openssl pkeyutl -sign -inkey my-key.pem -in digest.bin -out sig.der
# 4. Embed the signature.
go run seedhammer.com/cmd/picosign sign -pubkey "$PUB" -sig "$(xxd -p -c 256 sig.der)" -sigfmt der "$FW"
```

Check the result:

```sh
picotool info -a "$FW"
```

Expect exactly two metadata blocks, `signature: verified`, and your own X‖Y bytes on the `public key:` line. The same four commands re-sign any UF2 whose signature section is present, including published builds and official releases, and the `openssl pkeyutl` step can run on an offline machine or an HSM: only `digest.bin` travels.

## Step 5: Flash the signed image

The reliable way, from BOOTSEL:

```sh
picotool load --verify seedhammerii-<version>.uf2
picotool reboot
```

Copying the UF2 to the mass-storage volume also works; on an SH2 that's `/Volumes/SHII` on macOS or `/media/$USER/SHII` on Linux. macOS `cp` sometimes stalls without flashing anything; if the volume is still mounted after a minute, use `picotool load`.

Success: the volume disappears and stays gone, and the LCD shows the normal startup screen, with an `(UNLOCKED)` suffix on the version string (expected; see below). A dark screen with no BOOTSEL volume usually just needs a full unplug-replug. If the device instead reappears in BOOTSEL, the boot ROM rejected the image; see troubleshooting.

## Step 6: Revoke the SeedHammer key (optional)

If you want a device that boots only firmware you've signed, permanently invalidate slot 0:

```sh
# Re-enter BOOTSEL first
picotool otp set -s BOOT_FLAGS1.KEY_INVALID 0x1
picotool reboot
```

`KEY_INVALID` takes precedence over `KEY_VALID` and is irreversible. After this, the official SeedHammer release builds will no longer boot on this device. **Do this only after you've verified step 5 worked.** A safer choice is to leave slot 0 valid so the device retains dual-trust during testing.

## What changed inside the SH2

After step 5:

- `CRIT1.SECURE_BOOT_ENABLE = 1` (unchanged, was already set)
- `BOOT_FLAGS1.KEY_VALID = 0x3` (slots 0 and 1)
- `BOOTKEY0` = SeedHammer fingerprint
- `BOOTKEY1` = your fingerprint
- Page locks unchanged: the `BOOTKEY` hash rows live in OTP page 2 (rows 0x080-0x0bf), `BOOT_FLAGS1` and `CRIT1` in page 1 (rows 0x040-0x04d). Both pages are bootloader read-write by default (RP2350 datasheet §13.5.5); SeedHammer's [`LockBoot`](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go#L510) never calls `picotool otp permissions`.
- Boot ROM accepts a signature from either slot
- The firmware's internal secure-boot flag (`gui.FeatureSecureBoot`) will be off, and the version string in the UI gains an `(UNLOCKED)` suffix. [`isSecureBootEnabled()`](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go#L712) requires exactly one valid slot containing the official hash. The device is still cryptographically signature-locked; the flag change is cosmetic.

`LockBoot` performs only steps 1, 2, and 7 of the [RP2350 datasheet §10.5 procedure](https://datasheets.raspberrypi.com/rp2350/rp2350-datasheet.pdf). It skips step 3 (`KEY_INVALID` for unused slots). That's why slots 1-3 are available to you here.

## Troubleshooting

**"No accessible RP-series devices in BOOTSEL mode were found"**: the device isn't in BOOTSEL. Hold the button and replug. A device that is listed but can't be opened is a USB permission problem (udev rules on Linux, `sudo` on macOS).

**`picotool otp load` fails with "Attempted to clear bits in OTP row(s)"**: `otp load` replaces whole fields, so writing `"key_valid": 2` over an existing `0x1` would clear bit 0 and is refused. Use `picotool otp set -s BOOT_FLAGS1.KEY_VALID 0x2`, or load the combined value `"key_valid": 3`. Nothing was written.

**`BOOT_FLAGS1.KEY_VALID` still shows `0x1` after loading**: the JSON had no `boot_flags1` entry. Set the bit as in step 3.

**The LCD stays dark but the device does not reappear in BOOTSEL**: the image booted; the display didn't come up. Unplug the device completely and plug it back in. A rejected signature looks different: the device returns to BOOTSEL.

**The device falls back to BOOTSEL after reboot**: the boot ROM rejected the image. Two known causes:

1. *Three metadata blocks* in `picotool info -a`: the image was sealed twice. Rebuild and sign with the `picosign` flow from step 4.
2. *Signature/key mismatch*: `picotool info -a <file>` prints the image's `public key:` as 64 bytes of X‖Y. SHA-256 of exactly those 64 bytes must equal the hash in your OTP slot (row *N* = digest bytes 2*N*+1 then 2*N*). `picosign extract` prints the signature, not the public key.

**"OTP write failed: not permitted"**: an OTP page has been locked by a previous `picotool otp permissions` call — page 2 guards the `BOOTKEY` rows, page 1 guards `BOOT_FLAGS1`/`CRIT1`. That shouldn't happen on a stock SeedHammer-locked device. See [RP2350 datasheet §13.5 "Page locks"](https://datasheets.raspberrypi.com/rp2350/rp2350-datasheet.pdf) and the [picotool `permissions` docs](https://github.com/raspberrypi/picotool#permissions).

**A manually computed hash doesn't match what picotool wrote**: the hash format is **SHA-256 of the uncompressed 64-byte X‖Y pubkey** ([`picobin/picobin.go:103`](https://github.com/seedhammer/seedhammer/blob/main/picobin/picobin.go#L103), [`cmd/picosign/main.go:142-143`](https://github.com/seedhammer/seedhammer/blob/main/cmd/picosign/main.go#L142-L143)). The 33-byte compressed form produces the wrong hash and silently poisons the slot.

## References

SeedHammer source (upstream repo, `main` branch as of v1.4.2-5-g5f67fde; line numbers shift over time):

- [`cmd/controller/platform_sh2.go`](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go): [`LockBoot` at L510](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go#L510), [`writeOTPValues` at L666](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go#L666), [`isSecureBootEnabled` at L712](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go#L712), [hardcoded `signKeyHash` at L70](https://github.com/seedhammer/seedhammer/blob/main/cmd/controller/platform_sh2.go#L70)
- [`driver/otp/otp.go`](https://github.com/seedhammer/seedhammer/blob/main/driver/otp/otp.go): [`AddBootKey` (best-fit rule) at L102](https://github.com/seedhammer/seedhammer/blob/main/driver/otp/otp.go#L102), [`WriteBootKey` at L159](https://github.com/seedhammer/seedhammer/blob/main/driver/otp/otp.go#L159), [`IsSecureBootEnabled` at L97](https://github.com/seedhammer/seedhammer/blob/main/driver/otp/otp.go#L97)
- [`gui/gui.go`](https://github.com/seedhammer/seedhammer/blob/main/gui/gui.go): [`command: lock-boot` dispatch at L1415](https://github.com/seedhammer/seedhammer/blob/main/gui/gui.go#L1415), [`FeatureSecureBoot` at L2336](https://github.com/seedhammer/seedhammer/blob/main/gui/gui.go#L2336)
- [`picobin/picobin.go`](https://github.com/seedhammer/seedhammer/blob/main/picobin/picobin.go): [SIGNATURE item pubkey write at L103](https://github.com/seedhammer/seedhammer/blob/main/picobin/picobin.go#L103), `HashData` (signed coverage) at L300
- [`cmd/picosign/main.go`](https://github.com/seedhammer/seedhammer/blob/main/cmd/picosign/main.go): `hash`, `sign`, and `extract` subcommands; [X/Y assembly at L142-143](https://github.com/seedhammer/seedhammer/blob/main/cmd/picosign/main.go#L142-L143)
- [`flake.nix`](https://github.com/seedhammer/seedhammer/blob/main/flake.nix): `build-firmware`, `copy-signature`, dummy PEM

Upstream tooling and silicon docs:

- [picotool README, OTP section](https://github.com/raspberrypi/picotool#otp)
- [picotool releases (pre-built binaries)](https://github.com/raspberrypi/pico-sdk-tools/releases)
- [RP2350 datasheet (PDF)](https://datasheets.raspberrypi.com/rp2350/rp2350-datasheet.pdf): §5.10.1 "Secure boot", §10.5 "Secure boot enable procedure", §13.4 "Critical flags" (CRIT1), §13.5 "Page locks", §13.10 (BOOT_FLAGS1 register listing)
- [Raspberry Pi RP2350 product page](https://www.raspberrypi.com/products/rp2350/)
- [pico-examples (placeholder ELFs for `picotool seal`)](https://github.com/raspberrypi/pico-examples)

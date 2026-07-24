package engrave

// SH2Millimeter is the SeedHammer II's machine units per millimeter: 200 full
// steps per revolution, 8 mm per revolution, 256 microsteps => 6400. It is a
// const so cmd/controller can compile-assert its hardware-derived mm against it.
const SH2Millimeter = 200 / 8 * 256

// SH2Params is the single source of the SeedHammer II engraver profile: stroke
// width, stepper speeds, acceleration, and jerk in machine units. cmd/controller
// drives the device with these; the host tools (cmd/svgplate, cmd/textplate, and
// Studio's cost model) validate and plan against the same numbers, so what they
// accept matches exactly what the device engraves. Do not re-declare these.
var SH2Params = Params{
	StrokeWidth: int(0.3 * SH2Millimeter),
	Millimeter:  SH2Millimeter,
	StepperConfig: StepperConfig{
		TicksPerSecond: 30 * SH2Millimeter,
		Speed:          30 * SH2Millimeter,
		EngravingSpeed: 8 * SH2Millimeter,
		Acceleration:   250 * SH2Millimeter,
		Jerk:           2600 * SH2Millimeter,
	},
}

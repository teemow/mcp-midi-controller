// Package scene defines the scene model and persistence. A scene is a named
// snapshot of only the controls that have been explicitly set, so scenes stay
// small, partial and layerable. For preset-based devices it stores the program
// number plus CC overrides.
package scene

// RecallMode controls how a scene is applied.
type RecallMode string

const (
	// Additive applies the scene's values over the current state (good for
	// stacking a base tone + a per-section overlay).
	Additive RecallMode = "additive"
	// Exact resets each referenced device to exactly the scene's values.
	Exact RecallMode = "exact"
)

// Scene is a named, partial snapshot of the rig.
type Scene struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`

	// Devices maps logical device name -> control name -> value. Only controls
	// that were set are present.
	Devices map[string]map[string]any `yaml:"devices"`
}

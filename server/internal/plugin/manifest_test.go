package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescriptor_ParsesV2(t *testing.T) {
	// slots/permissions remain in the raw JSON intentionally: they are removed from
	// Descriptor but still present in old manifests — json.Unmarshal must not error.
	raw := `{
	  "id":"voice-whisper","name":"Voice (Whisper)","version":"1.2.0",
	  "capabilities":["route_extension"],"addr":"127.0.0.1:19010","command":["./voice-whisper"],
	  "slots":[{"slot":"agent-toolbar","priority":100,"mode":"extend"}],
	  "settings":[{"key":"endpoint","type":"url","label":"Endpoint"},
	              {"key":"apiKey","type":"string","label":"API Key","secret":true}],
	  "lifecycle":{"install":"/lifecycle/install","activate":"/lifecycle/activate"},
	  "permissions":["net"]
	}`
	var d Descriptor
	require.NoError(t, json.Unmarshal([]byte(raw), &d))
	assert.Equal(t, "Voice (Whisper)", d.Name)
	require.Len(t, d.Settings, 2)
	assert.True(t, d.Settings[1].Secret)
	assert.Equal(t, "/lifecycle/activate", d.Lifecycle.Activate)
}

func TestDescriptor_BackwardCompatV1(t *testing.T) {
	// An old manifest with no v2 fields must still parse, with zero-value v2 fields.
	raw := `{"id":"old","capabilities":["auth_provider"],"addr":"127.0.0.1:9000","command":["./old"]}`
	var d Descriptor
	require.NoError(t, json.Unmarshal([]byte(raw), &d))
	assert.Equal(t, "old", d.ID)
	assert.Empty(t, d.Settings)
	assert.Empty(t, d.Name)
}

func TestDescriptor_LegacySlotsAndPermissionsIgnored(t *testing.T) {
	// A manifest carrying the removed slots/permissions fields must parse without
	// error — json.Unmarshal ignores unknown JSON fields by default.
	raw := `{
	  "id":"legacy-plugin","capabilities":["route_extension"],"addr":"127.0.0.1:19020",
	  "slots":[{"slot":"agent-toolbar","priority":5,"mode":"override"}],
	  "permissions":["network:outbound","fs:read"]
	}`
	var d Descriptor
	require.NoError(t, json.Unmarshal([]byte(raw), &d))
	assert.Equal(t, "legacy-plugin", d.ID)
	assert.Equal(t, []string{"route_extension"}, d.Capabilities)
}

// A module states which core contract it was built against. The field is
// required and checked at load: every comparable plugin ecosystem shipped this
// as optional first and retrofitted it under pressure, because a module that
// cannot say what it expects fails at runtime instead of at install time.
func TestDescriptor_Validate(t *testing.T) {
	valid := Descriptor{Contract: CurrentContract, ID: "obsidian", Name: "Obsidian"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a complete manifest must validate: %v", err)
	}

	cases := []struct {
		name string
		desc Descriptor
		want string
	}{
		{"no contract", Descriptor{ID: "obsidian", Name: "Obsidian"}, "contract"},
		{"future contract", Descriptor{Contract: CurrentContract + 1, ID: "obsidian", Name: "Obsidian"}, "contract"},
		{"no name", Descriptor{Contract: CurrentContract, ID: "obsidian"}, "name"},
		{"no id", Descriptor{Contract: CurrentContract, Name: "Obsidian"}, "id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.desc.Validate()
			if err == nil {
				t.Fatalf("%s must be refused", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name the offending field %q", err, c.want)
			}
		})
	}
}

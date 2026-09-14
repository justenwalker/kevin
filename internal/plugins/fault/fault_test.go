package fault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Step{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "containers")
}

func TestKindIsAction(t *testing.T) {
	assert.Equal(t, plugin.StepKindAction, Step{}.Kind(),
		"a fault step tells the relay how to treat a namespace but owns no lifecycle")
}

func TestStepDoesNotImplementDowner(t *testing.T) {
	_, ok := any(Step{}).(plugin.Downer)
	assert.False(t, ok, "the engine clears an applied fault at teardown independent of a plugin Down")
}

func TestStepIsIdempotent(t *testing.T) {
	assert.True(t, Step{}.Idempotent())
}

func TestDecode(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)
		assert.Equal(t, config{}, cfg)
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte(`{`))
		assert.Error(t, err)
	})

	t.Run("every field round-trips", func(t *testing.T) {
		cfg, err := decode([]byte(`{
			"containers": ["worker_a", "worker_b"], "interface": "eth1",
			"delay_ms": 500, "jitter_ms": 100, "loss_percent": 10,
			"corrupt_percent": 1, "duplicate_percent": 2, "reorder_percent": 3
		}`))
		require.NoError(t, err)
		assert.Equal(t, config{
			Containers: []string{"worker_a", "worker_b"}, Interface: "eth1",
			DelayMS: 500, JitterMS: 100, LossPercent: 10,
			CorruptPercent: 1, DuplicatePercent: 2, ReorderPercent: 3,
		}, cfg)
	})
}

func TestValidateImpairment(t *testing.T) {
	t.Run("rejects an all-zero config", func(t *testing.T) {
		assert.ErrorIs(t, validateImpairment(config{}), ErrNoImpairment)
	})

	tests := []struct {
		name string
		cfg  config
	}{
		{name: "delay", cfg: config{DelayMS: 100}},
		{name: "loss", cfg: config{LossPercent: 1}},
		{name: "corrupt", cfg: config{CorruptPercent: 1}},
		{name: "duplicate", cfg: config{DuplicatePercent: 1}},
		{name: "reorder", cfg: config{ReorderPercent: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name+" alone is enough", func(t *testing.T) {
			assert.NoError(t, validateImpairment(tt.cfg))
		})
	}
}

func TestResolveTargets(t *testing.T) {
	single := []plugin.StepContainers{
		{Step: "backend", Containers: []plugin.ContainerInfo{{ID: "abc", Name: "kevin-demo-backend", NetnsPath: "/proc/1/ns/net"}}},
	}
	multi := []plugin.StepContainers{
		{Step: "cluster", Containers: []plugin.ContainerInfo{
			{ID: "def", Name: "control-plane", NetnsPath: "/proc/2/ns/net"},
			{ID: "ghi", Name: "worker_a", NetnsPath: "/proc/3/ns/net"},
		}},
	}
	both := append(append([]plugin.StepContainers{}, single...), multi...)

	t.Run("unset containers returns every container of every needs step", func(t *testing.T) {
		got, err := resolveTargets(both, nil)
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})

	t.Run("a container's own name resolves it", func(t *testing.T) {
		got, err := resolveTargets(multi, []string{"worker_a"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "/proc/3/ns/net", got[0].NetnsPath)
	})

	t.Run("a single-container step's own name is a fallback alias", func(t *testing.T) {
		got, err := resolveTargets(single, []string{"backend"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "/proc/1/ns/net", got[0].NetnsPath)
	})

	t.Run("a multi-container step's own name is not an alias - it has no single container to mean", func(t *testing.T) {
		_, err := resolveTargets(multi, []string{"cluster"})
		assert.ErrorIs(t, err, ErrContainerNotFound)
	})

	t.Run("several names select several containers, any count", func(t *testing.T) {
		got, err := resolveTargets(both, []string{"backend", "worker_a"})
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	t.Run("an unknown name is not found", func(t *testing.T) {
		_, err := resolveTargets(multi, []string{"nonexistent"})
		assert.ErrorIs(t, err, ErrContainerNotFound)
	})

	t.Run("no needs step reporting any containers is an error even with containers unset", func(t *testing.T) {
		_, err := resolveTargets(nil, nil)
		assert.ErrorIs(t, err, ErrNoContainers)
	})

	t.Run("a real container Name always wins over a same-string step alias", func(t *testing.T) {
		// "worker_a" is both a real container's Name (in "other") and
		// "backend"'s own step name is never at stake here directly, but
		// this proves resolveTargets doesn't let processing order decide:
		// register every real Name first, in one pass, before any alias.
		collide := []plugin.StepContainers{
			{Step: "worker_a", Containers: []plugin.ContainerInfo{{ID: "zzz", Name: "kevin-demo-worker_a", NetnsPath: "/proc/9/ns/net"}}},
			{Step: "cluster", Containers: []plugin.ContainerInfo{
				{ID: "def", Name: "control-plane", NetnsPath: "/proc/2/ns/net"},
				{ID: "ghi", Name: "worker_a", NetnsPath: "/proc/3/ns/net"},
			}},
		}
		got, err := resolveTargets(collide, []string{"worker_a"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "/proc/3/ns/net", got[0].NetnsPath, "the real container named worker_a must win over the step named worker_a")
	})
}

func TestUpBuildsNetworkFaults(t *testing.T) {
	single := []plugin.StepContainers{
		{Step: "backend", Containers: []plugin.ContainerInfo{{ID: "abc", Name: "kevin-demo-backend", NetnsPath: "/proc/1/ns/net"}}},
	}
	multi := []plugin.StepContainers{
		{Step: "cluster", Containers: []plugin.ContainerInfo{
			{ID: "def", Name: "control-plane", NetnsPath: "/proc/2/ns/net"},
			{ID: "ghi", Name: "worker_a", NetnsPath: "/proc/3/ns/net"},
		}},
	}

	t.Run("a single resolved container gets the step's own name as ID", func(t *testing.T) {
		result, err := Step{}.Up(t.Context(), &plugin.UpRequest{
			Step:       "backend_fault",
			Containers: single,
			Config:     []byte(`{"interface": "eth1", "delay_ms": 500, "loss_percent": 10}`),
		}, noopEmitter{})
		require.NoError(t, err)
		require.Len(t, result.Faults, 1)
		assert.Equal(t, plugin.NetworkFault{
			ID: "backend_fault", NetnsPath: "/proc/1/ns/net", Interface: "eth1",
			DelayMS: 500, LossPercent: 10,
		}, result.Faults[0])
	})

	t.Run("several resolved containers each get a step/name ID", func(t *testing.T) {
		result, err := Step{}.Up(t.Context(), &plugin.UpRequest{
			Step:       "cluster_fault",
			Containers: multi,
			Config:     []byte(`{"loss_percent": 50}`),
		}, noopEmitter{})
		require.NoError(t, err)
		require.Len(t, result.Faults, 2)
		ids := []string{result.Faults[0].ID, result.Faults[1].ID}
		assert.ElementsMatch(t, []string{"cluster_fault/control-plane", "cluster_fault/worker_a"}, ids)
	})

	t.Run("rejects an all-zero config before resolving any target", func(t *testing.T) {
		_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
			Step:       "backend_fault",
			Containers: single,
			Config:     []byte(`{}`),
		}, noopEmitter{})
		assert.ErrorIs(t, err, ErrNoImpairment)
	})
}

type noopEmitter struct{}

func (noopEmitter) Log(string, string)            {}
func (noopEmitter) Progress(string, int64, int64) {}

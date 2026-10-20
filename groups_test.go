package nestthermostat

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	spi "github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/group"
)

// registeringContainer takes the registrations of entities and of their views.
type registeringContainer struct {
	spi.IPMAASContainer

	ids         []string
	types       map[string]reflect.Type
	views       map[string]spi.EntityViewFunc
	deregistred []string
}

func (c *registeringContainer) RegisterEntity(uniqueData string, entityType reflect.Type, _ string, _ spi.EntityStubFactoryFunc) (string, error) {
	id := "nest_" + uniqueData

	if c.types == nil {
		c.types = map[string]reflect.Type{}
	}

	c.types[id] = entityType
	c.ids = append(c.ids, id)

	return id, nil
}

func (c *registeringContainer) RegisterEntityView(id string, view spi.EntityViewFunc) error {
	if c.views == nil {
		c.views = map[string]spi.EntityViewFunc{}
	}

	c.views[id] = view

	return nil
}

func (c *registeringContainer) DeregisterEntity(id string) error {
	c.deregistred = append(c.deregistred, id)

	return nil
}

func TestGroups_AThermostatHandleNamesItsEntityAndItsViewIsItsCard(t *testing.T) {
	cfg := NewPluginConfig()
	handle := cfg.AddThermostat("device-1", "Hallway")

	if _, ok := handle.RegisteredId(); ok {
		t.Fatal("the handle has an id before the plugin has registered anything")
	}

	if cfg.AddThermostat("device-1", "Hallway") != handle {
		t.Error("the same device should have the same handle")
	}

	container := &registeringContainer{}
	p := NewPlugin(cfg).(*plugin)
	p.container = container
	p.log = slog.New(slog.DiscardHandler)

	p.registerPreconfiguredThermostats()

	id, ok := handle.RegisteredId()
	if !ok || id != "nest_device-1" {
		t.Fatalf("%q %v", id, ok)
	}

	view, err := container.views[id]()
	if err != nil {
		t.Fatal(err)
	}

	card, isCard := view.Value.(*data.ThermostatData) // what the renderer takes
	if !isCard || card.Name != "Hallway" || view.Key != "nest/device-1" || view.Category != group.CategoryThermometers {
		t.Errorf("%#v", view)
	}

	p.deregisterEntities()

	if _, ok := handle.RegisteredId(); ok {
		t.Error("the handle still has an id after the entity was deregistered")
	}

	if len(container.deregistred) != 1 {
		t.Errorf("%v", container.deregistred)
	}
}

func TestGroups_AThermostatThatIsNotConfiguredHasAViewToo(t *testing.T) {
	container := &registeringContainer{}
	p := NewPlugin(NewPluginConfig()).(*plugin)
	p.container = container
	p.log = slog.New(slog.DiscardHandler)

	p.registerPreconfiguredThermostats() // none configured
	p.registerNewThermostat(entities.NewNestThermostat("found-1", "Upstairs", true))

	if len(container.views) != 1 {
		t.Errorf("%v", container.views)
	}
}

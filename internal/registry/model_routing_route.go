package registry

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelrouting"
)

// ModelRoutingKey validates the registry-owned identity required by the model
// routing bootstrap and returns its canonical route key.
func (route RegisteredRouteSnapshot) ModelRoutingKey(index int) (modelrouting.RouteKey, error) {
	if route.Model == nil {
		return modelrouting.RouteKey{}, fmt.Errorf("registered route %d has no model facts", index)
	}
	if strings.HasPrefix(strings.ToLower(route.RuntimeModelID), "aihub-") {
		return modelrouting.RouteKey{}, fmt.Errorf("registered route %d contains a managed alias before projection", index)
	}
	facts := []struct {
		name  string
		value string
	}{
		{name: "catalog provider", value: route.Model.CatalogProviderID},
		{name: "catalog model", value: route.Model.CatalogModelID},
		{name: "catalog route provider", value: route.Model.CatalogRouteProviderID},
		{name: "catalog route model", value: route.Model.CatalogRouteModelID},
		{name: "route channel", value: route.RouteChannel},
		{name: "runtime model", value: route.RuntimeModelID},
	}
	for _, fact := range facts {
		if fact.value == "" || fact.value != strings.TrimSpace(fact.value) || fact.value == "unknown" {
			return modelrouting.RouteKey{}, fmt.Errorf("registered route %d has invalid %s fact", index, fact.name)
		}
	}
	if len(route.Model.Protocols) == 0 {
		return modelrouting.RouteKey{}, fmt.Errorf("registered route %d has no explicit protocols", index)
	}
	for _, protocol := range route.Model.Protocols {
		if protocol == "" || protocol != strings.TrimSpace(protocol) || protocol == "unknown" {
			return modelrouting.RouteKey{}, fmt.Errorf("registered route %d has an invalid protocol", index)
		}
	}
	return modelrouting.RouteKey{
		ModelKey: modelrouting.ModelKey{
			CatalogProviderID: route.Model.CatalogProviderID,
			CanonicalModelID:  route.Model.CatalogModelID,
		},
		RouteChannel: route.RouteChannel,
	}, nil
}

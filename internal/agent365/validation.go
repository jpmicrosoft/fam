package agent365

import (
	"fmt"
	"strings"
)

// ValidationCheck is one safe readiness observation about a blueprint.
type ValidationCheck struct {
	Name     string `json:"name" yaml:"name"`
	Status   string `json:"status" yaml:"status"`
	Details  string `json:"details" yaml:"details"`
	Blocking bool   `json:"blocking" yaml:"blocking"`
}

// ValidationResult summarizes blueprint readiness without claiming that
// Foundry supports binding the blueprint.
type ValidationResult struct {
	Valid       bool                    `json:"valid" yaml:"valid"`
	Blueprint   Blueprint               `json:"blueprint" yaml:"blueprint"`
	Permissions []InheritablePermission `json:"inheritablePermissions" yaml:"inheritablePermissions"`
	Checks      []ValidationCheck       `json:"checks" yaml:"checks"`
}

// Validate evaluates documented blueprint properties. All three inheritance
// modes are accepted; the result does not infer support for a Foundry write
// operation.
func Validate(
	blueprint Blueprint,
	permissions []InheritablePermission,
) ValidationResult {
	result := ValidationResult{
		Valid:       true,
		Blueprint:   blueprint,
		Permissions: append([]InheritablePermission(nil), permissions...),
	}
	add := func(name, status, details string, blocking bool) {
		result.Checks = append(result.Checks, ValidationCheck{
			Name: name, Status: status, Details: details, Blocking: blocking,
		})
		if blocking {
			result.Valid = false
		}
	}

	disabled := strings.TrimSpace(blueprint.DisabledByMicrosoftStatus)
	switch {
	case disabled == "", strings.EqualFold(disabled, "NotDisabled"):
		add("microsoftStatus", "passed", "blueprint is not disabled by Microsoft", false)
	default:
		add(
			"microsoftStatus",
			"failed",
			fmt.Sprintf("blueprint status is %s", disabled),
			true,
		)
	}

	if len(blueprint.ManagerApplications) == 0 {
		add(
			"managerApplications",
			"warning",
			"no Microsoft first-party manager application is exposed; automatic management compatibility cannot be confirmed",
			false,
		)
	} else {
		add(
			"managerApplications",
			"passed",
			fmt.Sprintf("%d manager application(s) are configured", len(blueprint.ManagerApplications)),
			false,
		)
	}

	requested := 0
	for _, resource := range blueprint.RequiredResourceAccess {
		requested += len(resource.ResourceAccess)
	}
	if requested == 0 {
		add(
			"requiredResourceAccess",
			"warning",
			"the blueprint requests no resource permissions",
			false,
		)
	} else {
		add(
			"requiredResourceAccess",
			"passed",
			fmt.Sprintf("%d permission(s) are requested across %d resource application(s)", requested, len(blueprint.RequiredResourceAccess)),
			false,
		)
	}

	if len(permissions) == 0 {
		add(
			"inheritablePermissions",
			"warning",
			"no inheritable permission configuration is present",
			false,
		)
	} else {
		validModes := 0
		for _, permission := range permissions {
			switch normalizedInheritanceKind(permission.InheritableScopes.Kind) {
			case "allallowed", "enumerated", "none":
				validModes++
			default:
				add(
					"inheritablePermissions",
					"failed",
					fmt.Sprintf(
						"resource application %s uses unknown inheritance kind %q",
						permission.ResourceAppID,
						permission.InheritableScopes.Kind,
					),
					true,
				)
			}
		}
		if validModes == len(permissions) {
			add(
				"inheritablePermissions",
				"passed",
				fmt.Sprintf("%d documented inheritance configuration(s) are present", validModes),
				false,
			)
		}
	}

	return result
}

func normalizedInheritanceKind(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", ""))
}

package remote

import "testing"

func TestDeliveryReadAllowedButLocalLifecycleMutationNotExposed(t *testing.T) {
	for _, role := range []string{"observer", "worker", "controller"} {
		tools := Tools(role)
		if !contains(tools, "squad_dispatch_delivery") || !contains(tools, "squad_dispatch_retirement") {
			t.Fatal("missing read projection", role)
		}
		for _, name := range []string{"squad_closeout", "squad_operation_run", "squad_dispatch_retire_partial", "squad_dispatch_retire_client"} {
			if contains(tools, name) {
				t.Fatal("local mutation exposed remotely", role, name)
			}
		}
	}
}

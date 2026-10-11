package workspacelink
import("fmt";"testing";"github.com/multica-ai/multica/server/internal/permission")
func TestDeliveryProbe(t *testing.T){
 fmt.Printf("viewer_agent=%v guest=%v source_agent=%v human=%v\n",Decide(Op("deliver"),SideViewer,Actor{Role:permission.RoleMember,IsAgent:true}),Decide(Op("deliver"),SideViewer,Actor{Role:permission.RoleGuest,IsAgent:true}),Decide(Op("deliver"),SideSource,Actor{Role:permission.RoleOwner,IsAgent:true}),Decide(Op("deliver"),SideViewer,Actor{Role:permission.RoleOwner}))
}

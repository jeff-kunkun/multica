package workspacelink
import("fmt";"reflect";"testing";"github.com/multica-ai/multica/server/internal/permission")
func TestProbe(t *testing.T){
a:=Actor{Role:permission.RoleAdmin};f:=reflect.ValueOf(&a).Elem().FieldByName("SourceRole");if f.IsValid(){f.SetString("owner")}
fmt.Printf("viewer admin + source owner: %v\n",Decide(OpUpdateProjects,SideViewer,a))
a.IsAgent=true;fmt.Printf("agent with same roles: %v\n",Decide(OpUpdateProjects,SideViewer,a))
}

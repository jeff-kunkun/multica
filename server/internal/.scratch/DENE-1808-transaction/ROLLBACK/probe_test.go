package workspacelink
import("fmt";"testing")
func TestDeliveryProbe(t *testing.T){
 _,_,send:=matchManagedRoute("POST","/api/chat/sessions/10000000-0000-0000-0000-000000000001/messages")
 _,_,list:=matchManagedRoute("GET","/api/chat/directory")
 _,_,create:=matchManagedRoute("POST","/api/chat/sessions")
 fmt.Printf("managed_chat_send=%v managed_chat_list=%v create_chat=%v\n",send,list,create)
}

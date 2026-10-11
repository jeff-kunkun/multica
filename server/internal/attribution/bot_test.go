package attribution

import (
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
)

func TestBotAttributionPreservesPrincipalWithoutHumanSpeech(t *testing.T) {
	bot := uid(0x42)
	name := pgtype.Text{String: "Research Bot", Valid: true}
	for _, result := range []Result{
		ClassifyComment(CommentFacts{CommentID: comment, AuthorType: "member", AuthorID: human, ViaBotID: bot, ViaBotName: name}, SourceCommentSource),
		ClassifyDirect(DirectFacts{IssueID: issue, ActorUserID: human, ViaBotID: bot, ViaBotName: name}),
	} {
		if result.Source != SourceViaBot || !result.Source.Precise() || result.UserID != human || result.AccountableUserID != human || result.ViaBotID != bot || result.ViaBotName != name {
			t.Fatalf("bot attribution lost principal or provenance: %+v", result)
		}
	}
}

func TestBotAttributionSurvivesAgentDelegation(t *testing.T) {
	bot := uid(0x42)
	name := pgtype.Text{String: "Historical Bot", Valid: true}
	result := ClassifyComment(CommentFacts{CommentID: comment, AuthorType: "agent", SourceTaskID: srcTask, ParentOriginator: human, ParentViaBotID: bot, ParentViaBotName: name}, SourceDelegation)
	if result.Source != SourceDelegation || result.ViaBotID != bot || result.ViaBotName != name || result.UserID != human {
		t.Fatalf("lost delegation provenance: %+v", result)
	}
}

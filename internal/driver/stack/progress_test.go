package stack

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

func TestIsInProgressErr(t *testing.T) {
	// 別プロセスが更新中のときの CFN の弾き文言。
	yes := errors.New("Stack [x] is in UPDATE_IN_PROGRESS state and can not be updated")
	if !isInProgressErr(yes) {
		t.Error("*_IN_PROGRESS を含むエラーは true のはず")
	}
	if isInProgressErr(nil) || isInProgressErr(errors.New("ValidationError: something else")) {
		t.Error("該当しないエラーは false のはず")
	}
}

func ev(logicalID, resType, status string) cfntypes.StackEvent {
	return cfntypes.StackEvent{
		LogicalResourceId: aws.String(logicalID),
		ResourceType:      aws.String(resType),
		ResourceStatus:    cfntypes.ResourceStatus(status),
	}
}

func TestFirstInProgress(t *testing.T) {
	// 新しい順。Lambda がまだ DELETE_IN_PROGRESS(VPC の ENI 待ちの典型)。
	events := []cfntypes.StackEvent{
		ev("Stack", "AWS::CloudFormation::Stack", "DELETE_IN_PROGRESS"), // スタック自身は除外
		ev("Fn", "AWS::Lambda::Function", "DELETE_IN_PROGRESS"),
		ev("Bucket", "AWS::S3::Bucket", "DELETE_COMPLETE"),
	}
	got := firstInProgress(events)
	if got != "AWS::Lambda::Function Fn (DELETE_IN_PROGRESS)" {
		t.Fatalf("待ち中の Lambda を返すはず: %q", got)
	}
}

func TestFirstInProgressUsesLatestPerResource(t *testing.T) {
	// 同一リソースの最新イベントが COMPLETE なら「待ち中」ではない。
	// 古い IN_PROGRESS を拾ってはいけない。
	events := []cfntypes.StackEvent{
		ev("Fn", "AWS::Lambda::Function", "DELETE_COMPLETE"),
		ev("Fn", "AWS::Lambda::Function", "DELETE_IN_PROGRESS"),
	}
	if got := firstInProgress(events); got != "" {
		t.Fatalf("最新が COMPLETE のリソースは返さないはず: %q", got)
	}
}

func TestFirstInProgressEmpty(t *testing.T) {
	if got := firstInProgress(nil); got != "" {
		t.Fatalf("空なら空文字のはず: %q", got)
	}
	// スタックだけが IN_PROGRESS のときも(配下リソースの手がかりが無い)空。
	only := []cfntypes.StackEvent{ev("Stack", "AWS::CloudFormation::Stack", "DELETE_IN_PROGRESS")}
	if got := firstInProgress(only); got != "" {
		t.Fatalf("スタック自身は除外するはず: %q", got)
	}
}

func TestFirstFailure(t *testing.T) {
	rr := func(logicalID, resType, status, reason string) cfntypes.StackEvent {
		e := ev(logicalID, resType, status)
		e.ResourceStatusReason = aws.String(reason)
		return e
	}
	// 新しい順。根本原因(Subscription の Topic 不在)より後に巻き添えが積まれる
	events := []cfntypes.StackEvent{
		rr("Stack", "AWS::CloudFormation::Stack", "ROLLBACK_IN_PROGRESS", "The following resource(s) failed"),
		rr("AppQueue", "AWS::SQS::Queue", "CREATE_FAILED", "Resource creation cancelled"),
		rr("PeerSubscription", "AWS::SNS::Subscription", "CREATE_FAILED", "Topic does not exist"),
		rr("AppQueue", "AWS::SQS::Queue", "CREATE_IN_PROGRESS", ""),
	}
	got := firstFailure(events)
	want := "AWS::SNS::Subscription PeerSubscription: Topic does not exist"
	if got != want {
		t.Fatalf("firstFailure = %q, want %q", got, want)
	}
	if firstFailure(nil) != "" {
		t.Fatal("no events should yield empty")
	}
	// 巻き添えしか無いときは空(嘘の根本原因を言わない)
	only := []cfntypes.StackEvent{rr("AppQueue", "AWS::SQS::Queue", "CREATE_FAILED", "Resource creation cancelled")}
	if firstFailure(only) != "" {
		t.Fatal("cancelled-only should yield empty")
	}
}

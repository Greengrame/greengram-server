package api

import (
	"testing"

	"github.com/iamxvbaba/td/tlprofile"
)

func TestFrozenOKIsReadOnly(t *testing.T) {
	for _, m := range []tlprofile.SemanticID{
		tlprofile.SemanticMethodMessagesSendMessage,
		tlprofile.SemanticMethodMessagesSendMedia,
		tlprofile.SemanticMethodMessagesEditMessage,
		tlprofile.SemanticMethodMessagesStartBot,
		tlprofile.SemanticMethodMessagesGetBotCallbackAnswer,
		tlprofile.SemanticMethodChannelsCreateChannel,
		tlprofile.SemanticMethodChannelsJoinChannel,
		tlprofile.SemanticMethodPaymentsGetPaymentForm,
		tlprofile.SemanticMethodPaymentsSendStarsForm,
		tlprofile.SemanticMethodPaymentsTransferStarGift,
		tlprofile.SemanticMethodPremiumApplyBoost,
		tlprofile.SemanticMethodAccountSetPrivacy,
		tlprofile.SemanticMethodUploadSaveFilePart,
	} {
		if frozenOK[m] {
			t.Errorf("%#x is open to frozen accounts", uint64(m))
		}
	}
	if !frozenOK[tlprofile.SemanticMethodMessagesGetHistory] || !frozenOK[tlprofile.SemanticMethodAuthLogOut] {
		t.Error("frozen accounts can't read or sign out")
	}
}

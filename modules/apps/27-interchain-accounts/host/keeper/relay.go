package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/gogo/protobuf/proto"

	"github.com/cosmos/ibc-go/v4/modules/apps/27-interchain-accounts/host/types"
	"github.com/cosmos/ibc-go/v4/modules/apps/27-interchain-accounts/logger"
	icatypes "github.com/cosmos/ibc-go/v4/modules/apps/27-interchain-accounts/types"
	channeltypes "github.com/cosmos/ibc-go/v4/modules/core/04-channel/types"
)

// OnRecvPacket handles a given interchain accounts packet on a destination host chain.
// If the transaction is successfully executed, the transaction response bytes will be returned.
func (k Keeper) OnRecvPacket(ctx sdk.Context, packet channeltypes.Packet) ([]byte, error) {
	var data icatypes.InterchainAccountPacketData

	logger.InitLogger()

	if err := icatypes.ModuleCdc.UnmarshalJSON(packet.GetData(), &data); err != nil {

		logger.LogError("Error occurred during UnmarshalJSON: ", err.Error())
		fmt.Println("Error occurred during UnmarshalJSON at OnRecvPacket")

		// UnmarshalJSON errors are indeterminate and therefore are not wrapped and included in failed acks
		return nil, sdkerrors.Wrapf(icatypes.ErrUnknownDataType, "cannot unmarshal ICS-27 interchain account packet data")
	}

	logger.LogInfo("packet data successfully marshalled")
	fmt.Println("packet data successfully marshalled")

	// For some reason the msg type is not being logged even though the transaction is succeeding
	// Let's log the msg outside the switch statement
	logger.LogInfo("un packing the msg type now")
	msgs, err := icatypes.DeserializeCosmosTx(k.cdc, data.Data)
	if err != nil {
		logger.LogInfo("Could not deserialize cosmos tx into msgs, error is:", err)
		fmt.Println("Could not deserialize cosmos tx into msgs")
		return nil, err
	}

	logger.LogInfo("How many messages we packed into IBC_packet.data:", len(msgs))
	msg0 := msgs[0]
	logger.LogInfo("msg0 as String is:", msg0.String()) // we can probably parse this string to obtain the protobuf.decode() value here
	logger.LogInfo("msg0 signers are:", msg0.GetSigners())
	logger.LogInfo("msg0 type URL is:", sdk.MsgTypeURL(msg0))

	for i, msg := range msgs {
		logger.LogInfo(fmt.Sprintf("Message %d: %s", i, msg.String()))
	}

	switch data.Type {
	case icatypes.EXECUTE_TX:
		msgs, err := icatypes.DeserializeCosmosTx(k.cdc, data.Data)
		if err != nil {
			logger.LogInfo("Could not deserialize cosmos tx into msgs, error is:", err)
			fmt.Println("Could not deserialize cosmos tx into msgs")
			return nil, err
		}

		txResponse, err := k.executeTx(ctx, packet.SourcePort, packet.DestinationPort, packet.DestinationChannel, msgs)
		if err != nil {
			logger.LogInfo("Transaction failed. Error:", err)
			return nil, err
		}
		logger.LogInfo("Transaction did not error. Tx response:", txResponse)

		return txResponse, nil
	default:
		return nil, icatypes.ErrUnknownDataType
	}
}

// executeTx attempts to execute the provided transaction. It begins by authenticating the transaction signer.
// If authentication succeeds, it does basic validation of the messages before attempting to deliver each message
// into state. The state changes will only be committed if all messages in the transaction succeed. Thus the
// execution of the transaction is atomic, all state changes are reverted if a single message fails.
func (k Keeper) executeTx(ctx sdk.Context, sourcePort, destPort, destChannel string, msgs []sdk.Msg) ([]byte, error) {
	channel, found := k.channelKeeper.GetChannel(ctx, destPort, destChannel)
	if !found {
		return nil, channeltypes.ErrChannelNotFound
	}

	if err := k.authenticateTx(ctx, msgs, channel.ConnectionHops[0], sourcePort); err != nil {
		return nil, err
	}

	txMsgData := &sdk.TxMsgData{
		Data: make([]*sdk.MsgData, len(msgs)),
	}

	// CacheContext returns a new context with the multi-store branched into a cached storage object
	// writeCache is called only if all msgs succeed, performing state transitions atomically
	cacheCtx, writeCache := ctx.CacheContext()
	for i, msg := range msgs {
		if err := msg.ValidateBasic(); err != nil {
			return nil, err
		}

		msgResponse, err := k.executeMsg(cacheCtx, msg)
		if err != nil {
			return nil, err
		}

		txMsgData.Data[i] = &sdk.MsgData{
			MsgType: sdk.MsgTypeURL(msg),
			Data:    msgResponse,
		}

	}

	// NOTE: The context returned by CacheContext() creates a new EventManager, so events must be correctly propagated back to the current context
	ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())
	writeCache()

	txResponse, err := proto.Marshal(txMsgData)
	if err != nil {
		return nil, sdkerrors.Wrap(err, "failed to marshal tx data")
	}

	return txResponse, nil
}

// authenticateTx ensures the provided msgs contain the correct interchain account signer address retrieved
// from state using the provided controller port identifier
func (k Keeper) authenticateTx(ctx sdk.Context, msgs []sdk.Msg, connectionID, portID string) error {
	interchainAccountAddr, found := k.GetInterchainAccountAddress(ctx, connectionID, portID)
	if !found {
		return sdkerrors.Wrapf(icatypes.ErrInterchainAccountNotFound, "failed to retrieve interchain account on port %s", portID)
	}

	logger.InitLogger()
	logger.LogInfo("interchainAccountAddr is:", interchainAccountAddr)

	allowMsgs := k.GetAllowMessages(ctx)

	for i, allowMsg := range allowMsgs {
		logger.LogInfo(fmt.Sprintf("ICA Host Allowed message %d: %s", i, allowMsg))
	}
	logger.LogInfo("length of allowMsgs slice is", len(allowMsgs))
	logger.LogInfo("first allowed message is:", allowMsgs[0])

	// Based on the below code, how could the wild card of "*" possible work to allow all messages from all modules?
	// Does this wild card only work for latest ibc-go?

	for _, msg := range msgs {
		if !types.ContainsMsgType(allowMsgs, msg) {
			return sdkerrors.Wrapf(sdkerrors.ErrUnauthorized, "message type not allowed: %s", sdk.MsgTypeURL(msg))
		}

		for _, signer := range msg.GetSigners() {
			if interchainAccountAddr != signer.String() {
				return sdkerrors.Wrapf(sdkerrors.ErrUnauthorized, "unexpected signer address: expected %s, got %s", interchainAccountAddr, signer.String())
			}
		}
	}

	return nil
}

// Attempts to get the message handler from the router and if found will then execute the message.
// If the message execution is successful, the proto marshaled message response will be returned.
func (k Keeper) executeMsg(ctx sdk.Context, msg sdk.Msg) ([]byte, error) {

	logger.InitLogger()
	logger.LogInfo("the msg before it hits the handler is:", msg)
	logger.LogInfo("As string the msg before it hits the handler is:", msg.String())

	handler := k.msgRouter.Handler(msg)
	if handler == nil {
		return nil, icatypes.ErrInvalidRoute
	}

	res, err := handler(ctx, msg)
	if err != nil {
		return nil, err
	}

	// NOTE: The sdk msg handler creates a new EventManager, so events must be correctly propagated back to the current context
	ctx.EventManager().EmitEvents(res.GetEvents())

	return res.Data, nil
}

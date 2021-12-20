package types

import (
	"bytes"

	"github.com/ipfs/go-cid"
)

type BlockMsg struct {
	Header        *BlockHeader
	BlsMessages   []cid.Cid
	SecpkMessages []cid.Cid
	CrossMessages []cid.Cid
}

type OldBlockMsg struct {
	Header        *BlockHeader
	BlsMessages   []cid.Cid
	SecpkMessages []cid.Cid
}

func DecodeBlockMsg(b []byte) (*BlockMsg, error) {
	var bm BlockMsg
	if err := bm.UnmarshalCBOR(bytes.NewReader(b)); err != nil {
		// If we couldn't unmarshal the new version of block format,
		// we try with the old version.
		var obm OldBlockMsg
		if err := obm.UnmarshalCBOR(bytes.NewReader(b)); err != nil {
			return nil, err
		}
		bm.Header = obm.Header
		bm.BlsMessages = obm.BlsMessages
		bm.SecpkMessages = obm.SecpkMessages
	}

	return &bm, nil
}

func (bm *BlockMsg) Cid() cid.Cid {
	return bm.Header.Cid()
}

func (bm *BlockMsg) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := bm.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

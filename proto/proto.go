package proto

const (
	Start = 1
	Stop = 2
)

type Proto struct {
	Opt int
	Seq int64
}
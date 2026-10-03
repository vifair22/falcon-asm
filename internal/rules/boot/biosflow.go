package boot

import (
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
)

// The shared dataflow treats every int as clobbering every general
// register and DS/ES/FS/GS: sound, and enough for most rules. A loader's
// read is usually a retry loop (`retry: ... int $0x13; jc retry`), and with
// that model ES and BX, the read's destination, are unknown at the int on
// the second time round. The loader check therefore runs its own analysis
// in which an int clobbers what the BIOS table says that service changes
// (by AH, and AL where it selects), and everything only when AH is not
// known.

// defaultClobbers is what an int with an unknown service may change.
var defaultClobbers = []flow.Reg{flow.RAX, flow.RCX, flow.RDX, flow.RBX, flow.RBP, flow.RSI, flow.RDI,
	flow.ES, flow.DS, flow.FS, flow.GS}

// biosConfig models BIOS calls from the hardware tables.
func biosConfig(db *hwdb.DB) flow.Config {
	return flow.Config{
		IntClobbers: func(v uint64) ([]flow.Reg, bool) {
			if _, ok := db.BIOSVectorInfo(v); ok {
				return []flow.Reg{}, true // IntEffect clobbers per service
			}
			return nil, false
		},
		IntEffect: func(v uint64, st *flow.State) {
			vec, ok := db.BIOSVectorInfo(v)
			if !ok {
				return
			}
			ah, mAH := st.Part(flow.RAX, 8, 15)
			al, mAL := st.Part(flow.RAX, 0, 7)
			var svc *hwdb.BIOSService
			if mAH == 0xFF {
				svc, ok = db.LookupBIOS(v, ah, al, mAL == 0xFF)
			} else if s, found := db.LookupBIOS(v, 0, 0, false); found && s.AH == nil {
				svc, ok = s, true
			} else {
				ok = false
			}
			if !ok {
				for _, r := range defaultClobbers {
					st.Clobber(r, flow.DefMerged)
				}
				return
			}
			for _, name := range svc.AllClobbers(vec) {
				if fam, _, _, found := flow.RegOf(name); found && fam != flow.RSP {
					st.Clobber(fam, flow.DefMerged)
				}
			}
		},
	}
}

const biosFlowKey = "boot/biosflow"

// withBIOS returns a copy of f whose dataflow models BIOS calls from the
// tables; f itself when the analysis cannot be built.
func (f *facts) withBIOS(c *lint.Context) *facts {
	v := c.Memo(biosFlowKey, func() (out any) {
		defer func() {
			if recover() != nil {
				out = (*flow.Result)(nil)
			}
		}()
		return flow.Analyze(f.g, biosConfig(hw.DB(c.Tables)))
	})
	res, _ := v.(*flow.Result)
	if res == nil {
		return f
	}
	g := *f
	g.res = res
	return &g
}

package options

import "github.com/spf13/pflag"

type SearchOption struct {
	SearchHost string
	Token      string
}

func NewSearchOptions() *SearchOption {
	return &SearchOption{}
}

func (p *SearchOption) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&p.SearchHost, "searh-host", p.SearchHost, "The host of the search api")
	fs.StringVar(&p.Token, "searh-token", p.Token, "The token of the search api")
}

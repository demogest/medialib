package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/demogest/medialib/internal/config"
	"github.com/demogest/medialib/internal/media"
	"github.com/demogest/medialib/internal/s3"
)

// parse reads flags and positional arguments in any order (the standard flag package stops at the first positional).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func need(pos []string, n int, what string) error {
	if len(pos) < n {
		return fmt.Errorf("missing %s (see `medialib help`)", what)
	}
	return nil
}

func run(cmd string, args []string) error {
	cfg, err := config.Load(config.Home())
	if err != nil {
		return err
	}
	switch cmd {
	case "serve":
		return cmdServe(cfg, args, "server")
	case "desktop":
		return cmdServe(cfg, args, "desktop")
	case "compact":
		return cmdCompact(cfg, args)
	case "index":
		return cmdIndex(cfg, args)
	case "add":
		return cmdAdd(cfg, args)
	case "libraries":
		return cmdLibraries(cfg)
	case "connect":
		return cmdConnect(cfg, args)
	case "connections":
		return cmdConnections(cfg, args)
	case "import":
		return cmdImport(cfg, args)
	case "ls":
		return cmdLs(cfg, args)
	case "add-s3":
		return cmdAddS3(cfg, args)
	}
	return fmt.Errorf("unknown command %q (see `medialib help`)", cmd)
}

type printer struct{}

func (printer) State(_, line string)           { fmt.Println(line) }
func (printer) Plan(_ int, line string)        { fmt.Println(line) }
func (printer) Progress(_, _ int, line string) { fmt.Println(line) }
func (printer) Line(line string)               { fmt.Println(line) }

func cmdIndex(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	id := fs.String("library", "", "library id (default: the active one)")
	workers := fs.Int("workers", 0, "files indexed at once")
	limit := fs.Int("limit", 0, "index at most N new/changed files")
	force := fs.Bool("force", false, "re-index everything")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	lib, ok := cfg.Library(*id)
	if !ok {
		var ids []string
		for _, l := range cfg.Libraries() {
			ids = append(ids, l.ID)
		}
		return fmt.Errorf("Unknown library %q. Known: %s", *id, strings.Join(ids, ", "))
	}
	fmt.Printf("Library: %s [%s]\n", lib.Name, lib.ID)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ix := &media.Indexer{Cfg: cfg, Clients: config.NewClients(cfg)}
	return ix.Run(ctx, lib, media.Options{Workers: *workers, Limit: *limit, Force: *force}, printer{})
}

// cmdCompact converts the JPEG thumbnails of version 3.0 to WebP, about half the size, without reading any media.
func cmdCompact(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	id := fs.String("library", "", "library id (default: every library)")
	workers := fs.Int("workers", 0, "conversions at once")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var libs []config.Library
	if *id != "" {
		lib, ok := cfg.Library(*id)
		if !ok {
			return fmt.Errorf("Unknown library %q", *id)
		}
		libs = []config.Library{lib}
	} else {
		libs = cfg.Libraries()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, lib := range libs {
		fmt.Printf("%s [%s]\n", lib.Name, lib.ID)
		res, err := media.CompactThumbs(ctx, cfg, lib, *workers, func(done, total int) { fmt.Printf("\r  %d / %d", done, total) })
		fmt.Printf("\r  %s\n", media.FormatCompact(res))
		if err != nil {
			return err
		}
	}
	return nil
}

func cmdAdd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "PATH"); err != nil {
		return err
	}
	lib, err := cfg.AddLocalLibrary(pos[0], *name)
	if err != nil {
		return err
	}
	fmt.Printf("Added '%s' [%s] -> %s\nIndex it with: medialib index --library %s\n", lib.Name, lib.ID, lib.Path, lib.ID)
	return nil
}

func cmdLibraries(cfg *config.Config) error {
	active := cfg.Active()
	for _, lib := range cfg.Libraries() {
		d, err := media.LoadLibrary(cfg, lib)
		n := 0
		if err == nil {
			n = len(d.Items)
		}
		mark := " "
		if lib.ID == active {
			mark = "*"
		}
		fmt.Printf("%s %-24s %-7s %6d items  %s  (%s)\n", mark, lib.ID, lib.Type, n, config.Location(lib), lib.Name)
	}
	return nil
}

func cmdConnect(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	name := fs.String("name", "", "")
	provider := fs.String("provider", "other", "")
	endpoint := fs.String("endpoint", "", "")
	region := fs.String("region", "", "")
	ak := fs.String("access-key", "", "")
	sk := fs.String("secret-key", "", "prefer --secret-env: this ends up in your shell history")
	senv := fs.String("secret-env", "", "name of an environment variable holding the secret key")
	addressing := fs.String("addressing", "", "path | virtual | auto")
	bucket := fs.String("bucket", "", "default bucket, for keys that cannot list buckets")
	insecure := fs.Bool("insecure", false, "do not verify the TLS certificate")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *ak == "" {
		return errors.New("--access-key is required")
	}
	if _, ok := s3.ProviderByID(*provider); !ok {
		return fmt.Errorf("unknown provider %q", *provider)
	}
	verify := !*insecure
	rec, err := cfg.AddConnection(config.ConnectionForm{Name: name, Provider: provider, Endpoint: endpoint, Region: region, AccessKey: ak,
		SecretKey: sk, SecretKeyEnv: senv, Addressing: addressing, VerifyTLS: &verify, DefaultBucket: bucket})
	if err != nil {
		return err
	}
	fmt.Printf("Added connection '%s' [%s] -> %s\n", rec.Name, rec.ID, rec.Endpoint)
	return listConnections(cfg, true, rec.ID)
}

func cmdConnections(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("connections", flag.ContinueOnError)
	test := fs.Bool("test", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	return listConnections(cfg, *test, "")
}

func listConnections(cfg *config.Config, test bool, only string) error {
	conns := cfg.Connections()
	clients := config.NewClients(cfg)
	for _, c := range conns {
		if only != "" && c.ID != only {
			continue
		}
		label := c.Provider
		if p, ok := s3.ProviderByID(c.Provider); ok {
			label = p.Label
		}
		fmt.Printf("%-20s %-22s %s  (%s)\n", c.ID, label, c.Endpoint, c.Name)
		if test {
			client, err := clients.Get(c.ID)
			if err != nil {
				fmt.Printf("    FAILED: %s\n", err)
				continue
			}
			r := config.TestConnection(client, c.DefaultBucket)
			if r.OK {
				count := ""
				if r.Buckets != nil {
					count = fmt.Sprintf(", %d buckets", len(r.Buckets))
				}
				fmt.Printf("    OK in %d ms%s\n", *r.LatencyMS, count)
			} else {
				fmt.Printf("    FAILED: %s\n", r.Message)
			}
		}
	}
	if len(conns) == 0 {
		fmt.Println("No connections yet. Add one with `connect`, or see what can be imported with `import`.")
	}
	return nil
}

func cmdImport(cfg *config.Config, args []string) error {
	pos, err := parse(flag.NewFlagSet("import", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		drafts := cfg.Importable()
		for _, d := range drafts {
			ep := d.Endpoint
			if ep == "" {
				ep = "(AWS)"
			}
			fmt.Printf("%-24s %-45s %s\n", d.Source, ep, d.Label)
		}
		if len(drafts) == 0 {
			fmt.Println("Nothing to import: no S3 remotes in rclone, no ~/.aws/credentials, no AWS_* variables.")
		}
		return nil
	}
	rec, err := cfg.Adopt(pos[0])
	if err != nil {
		return err
	}
	fmt.Printf("Connection '%s' [%s] is ready (%s).\n", rec.Name, rec.ID, rec.Endpoint)
	return nil
}

func split(spec string) (conn, bucket, prefix string) {
	conn, rest, _ := strings.Cut(spec, ":")
	bucket, prefix, _ = strings.Cut(rest, "/")
	return
}

func cmdLs(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	rec := fs.Bool("r", false, "")
	fs.BoolVar(rec, "recursive", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "CONNECTION"); err != nil {
		return err
	}
	conn, bucket, prefix := split(pos[0])
	client, err := config.NewClients(cfg).Get(conn)
	if err != nil {
		return err
	}
	if bucket == "" {
		bs, err := client.ListBuckets()
		if err != nil {
			return err
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].Name < bs[j].Name })
		for _, b := range bs {
			created := b.Created
			if len(created) > 10 {
				created = created[:10]
			}
			fmt.Printf("%-12s %s\n", created, b.Name)
		}
		return nil
	}
	delim := "/"
	if *rec {
		delim = ""
	}
	token := ""
	for {
		page, err := client.ListObjects(bucket, prefix, delim, token, 1000)
		if err != nil {
			return err
		}
		for _, p := range page.Prefixes {
			fmt.Printf("%10s  %-20s %s\n", "", "", p)
		}
		for _, o := range page.Objects {
			fmt.Printf("%10s  %-20s %s\n", media.Human(o.Size), o.MTime, o.Key)
		}
		if page.Next == nil {
			return nil
		}
		token = *page.Next
	}
}

func cmdAddS3(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("add-s3", flag.ContinueOnError)
	name := fs.String("name", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "CONNECTION and BUCKET[/PREFIX]"); err != nil {
		return err
	}
	bucket, prefix, _ := strings.Cut(pos[1], "/")
	lib, err := cfg.AddS3Library(pos[0], bucket, prefix, *name)
	if err != nil {
		return err
	}
	fmt.Printf("Added '%s' [%s] -> %s\nIndex it with: medialib index --library %s\n", lib.Name, lib.ID, config.Location(lib), lib.ID)
	return nil
}

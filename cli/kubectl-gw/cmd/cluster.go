package cmd

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func NewClusterCMD(streams genericclioptions.IOStreams) *cobra.Command {
	o := NewOptions(streams)
	useCmd := &cobra.Command{
		Use:          "cluster ...",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}
			if err := o.Complete(args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run(c.Context(), streams)
		},
	}
	o.BindFlags(useCmd)
	return useCmd
}

// Options contains options common to most CLI plugins, including settings for connecting to kcp (kubeconfig, etc).
type Options struct {
	// Kubeconfig specifies kubeconfig file(s).
	Kubeconfig string
	// KubectlOverrides stores the extra client connection fields, such as context, user, etc.
	KubectlOverrides *clientcmd.ConfigOverrides

	genericclioptions.IOStreams

	// ClientConfig is the resolved cliendcmd.ClientConfig based on the client connection flags. This is only valid
	// after calling Complete.
	ClientConfig   clientcmd.ClientConfig
	startingConfig clientcmdapi.Config

	modifyConfig func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error

	Name string
}

// NewOptions provides an instance of Options with default values.
func NewOptions(streams genericclioptions.IOStreams) *Options {
	return &Options{
		KubectlOverrides: &clientcmd.ConfigOverrides{},
		modifyConfig: func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error {
			return clientcmd.ModifyConfig(configAccess, *newConfig, true)
		},
		IOStreams: streams,
	}
}

// BindFlags binds options fields to cmd's flagset.
func (o *Options) BindFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig, "path to the kubeconfig file")

	// We add only a subset of kubeconfig-related flags to the plugin.
	// All those with LongName == "" will be ignored.
	kubectlConfigOverrideFlags := clientcmd.RecommendedConfigOverrideFlags("")
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientCertificate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientKey.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.Impersonate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ImpersonateGroups.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.ClusterName.LongName = ""
	kubectlConfigOverrideFlags.Timeout.LongName = ""

	clientcmd.BindOverrideFlags(o.KubectlOverrides, cmd.PersistentFlags(), kubectlConfigOverrideFlags)
}

func (o *Options) Complete(args []string) error {
	if o.Name == "" && len(args) > 0 {
		o.Name = args[0]
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = o.Kubeconfig

	var err error
	o.ClientConfig = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, o.KubectlOverrides)
	o.startingConfig, err = o.ClientConfig.RawConfig()
	if err != nil {
		return err
	}
	return nil
}

// Validate validates the configured options.
func (o *Options) Validate() error {
	return nil
}

func (o *Options) Run(ctx context.Context, streams genericclioptions.IOStreams) error {
	name := o.Name
	newKubeConfig := o.startingConfig.DeepCopy()
	
	if _, found := o.startingConfig.Contexts[name]; found {
		newKubeConfig.CurrentContext = name
		if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
			return err
		}
		_, err := fmt.Fprintf(streams.Out, "Current cluster is the %q.\n", name)
		return err
	}

	// Store the currentContext content for later to set as previous context
	rootContext, found := o.startingConfig.Contexts["root"]
	if !found {
		return fmt.Errorf("root context not found")
	}
	rootCluster, found := o.startingConfig.Clusters[rootContext.Cluster]
	if !found {
		return fmt.Errorf("cluster %q not found in kubeconfig", rootContext.Cluster)
	}
	newCluster := *rootCluster
	newCluster.Server = rootCluster.Server + "/clusters/" + name
	newKubeConfig.Clusters[name] = &newCluster
	newContext := *rootContext
	newContext.Cluster = name
	newKubeConfig.Contexts[name] = &newContext
	newKubeConfig.CurrentContext = name
	if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
		return err
	}
	_, err := fmt.Fprintf(streams.Out, "Current cluster is the %q.\n", name)
	return err
}

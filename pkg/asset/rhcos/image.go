// Package rhcos contains assets for RHCOS.
package rhcos

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/coreos/stream-metadata-go/arch"
	"github.com/sirupsen/logrus"

	"github.com/openshift/installer/pkg/asset"
	"github.com/openshift/installer/pkg/asset/installconfig"
	"github.com/openshift/installer/pkg/rhcos"
	"github.com/openshift/installer/pkg/types"
	"github.com/openshift/installer/pkg/types/alibabacloud"
	"github.com/openshift/installer/pkg/types/aws"
	"github.com/openshift/installer/pkg/types/azure"
	"github.com/openshift/installer/pkg/types/baremetal"
	"github.com/openshift/installer/pkg/types/gcp"
	"github.com/openshift/installer/pkg/types/ibmcloud"
	"github.com/openshift/installer/pkg/types/libvirt"
	"github.com/openshift/installer/pkg/types/none"
	"github.com/openshift/installer/pkg/types/nutanix"
	"github.com/openshift/installer/pkg/types/openstack"
	"github.com/openshift/installer/pkg/types/ovirt"
	"github.com/openshift/installer/pkg/types/powervs"
	"github.com/openshift/installer/pkg/types/vsphere"
)

// Image is location of RHCOS image.
// This stores the location of the image based on the platform.
// eg. on AWS this contains ami-id, on Livirt this can be the URI for QEMU image etc.
type Image string

var _ asset.Asset = (*Image)(nil)

// Name returns the human-friendly name of the asset.
func (i *Image) Name() string {
	return "Image"
}

// Dependencies returns dependencies used by the RHCOS asset.
func (i *Image) Dependencies() []asset.Asset {
	return []asset.Asset{
		&installconfig.InstallConfig{},
	}
}

// Generate the RHCOS image location.
func (i *Image) Generate(p asset.Parents) error {
	if oi, ok := os.LookupEnv("OPENSHIFT_INSTALL_OS_IMAGE_OVERRIDE"); ok && oi != "" {
		logrus.Warn("Found override for OS Image. Please be warned, this is not advised")
		*i = Image(oi)
		return nil
	}

	ic := &installconfig.InstallConfig{}
	p.Get(ic)
	config := ic.Config
	osimage, err := osImage(config)
	if err != nil {
		return err
	}
	*i = Image(osimage)
	return nil
}

func osImage(config *types.InstallConfig) (string, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	archName := arch.RpmArch(string(config.ControlPlane.Architecture))

	st, err := rhcos.FetchCoreOSBuild(ctx)
	if err != nil {
		return "", err
	}
	streamArch, err := st.GetArchitecture(archName)
	if err != nil {
		return "", err
	}
	switch config.Platform.Name() {
	case aws.Name:
		if len(config.Platform.AWS.AMIID) > 0 {
			return config.Platform.AWS.AMIID, nil
		}
		region := config.Platform.AWS.Region
		if !rhcos.AMIRegions(config.ControlPlane.Architecture).Has(region) {
			const globalResourceRegion = "us-east-1"
			logrus.Debugf("No AMI found in %s. Using AMI from %s.", region, globalResourceRegion)
			region = globalResourceRegion
		}
		osimage, err := st.GetAMI(archName, region)
		if err != nil {
			return "", err
		}
		if region != config.Platform.AWS.Region {
			osimage = fmt.Sprintf("%s,%s", osimage, region)
		}
		return osimage, nil
	case gcp.Name:
		if streamArch.Images.Gcp != nil {
			img := streamArch.Images.Gcp
			return fmt.Sprintf("projects/%s/global/images/%s", img.Project, img.Name), nil
		}
		return "", fmt.Errorf("%s: No GCP build found", st.FormatPrefix(archName))
	case ibmcloud.Name:
		if a, ok := streamArch.Artifacts["ibmcloud"]; ok {
			return rhcos.FindArtifactURL(a)
		}
		return "", fmt.Errorf("%s: No ibmcloud build found", st.FormatPrefix(archName))
	case libvirt.Name:
		// 𝅘𝅥𝅮 Everything's going to be a-ok 𝅘𝅥𝅮
		if a, ok := streamArch.Artifacts["qemu"]; ok {
			return rhcos.FindArtifactURL(a)
		}
		return "", fmt.Errorf("%s: No qemu build found", st.FormatPrefix(archName))
	case ovirt.Name, openstack.Name:
		op := config.Platform.OpenStack
		if op != nil {
			if oi := op.ClusterOSImage; oi != "" {
				return oi, nil
			}
		}
		if a, ok := streamArch.Artifacts["openstack"]; ok {
			return rhcos.FindArtifactURL(a)
		}
		return "", fmt.Errorf("%s: No openstack build found", st.FormatPrefix(archName))
	case azure.Name:
		ext := streamArch.RHELCoreOSExtensions
		if config.Platform.Azure.CloudName == azure.StackCloud {
			return config.Platform.Azure.ClusterOSImage, nil
		}
		if ext == nil {
			return "", fmt.Errorf("%s: No azure build found", st.FormatPrefix(archName))
		}
		azd := ext.AzureDisk
		if azd == nil {
			return "", fmt.Errorf("%s: No azure build found", st.FormatPrefix(archName))
		}
		return azd.URL, nil
	case baremetal.Name:
		// Check for image URL override
		if oi := config.Platform.BareMetal.ClusterOSImage; oi != "" {
			return oi, nil
		}
		// Use image from release payload
		return "", nil
	case vsphere.Name:
		// Check for image URL override
		if config.Platform.VSphere.ClusterOSImage != "" {
			return config.Platform.VSphere.ClusterOSImage, nil
		}

		if a, ok := streamArch.Artifacts["vmware"]; ok {
			// for an unknown reason vSphere OVAs are not
			// integrity checked. Instead of going through
			// FindArtifactURL just create the URL here.
			artifact := a.Formats["ova"].Disk
			u, err := url.Parse(artifact.Location)

			if err != nil {
				return "", err
			}

			// Add the sha256 query to the url
			// This will later be used in pkg/tfvars/internal/cache/cache.go
			q := u.Query()
			q.Set("sha256", artifact.Sha256)

			u.RawQuery = q.Encode()

			return u.String(), nil
		}
		return "", fmt.Errorf("%s: No vmware build found", st.FormatPrefix(archName))
	case alibabacloud.Name:
		osimage, err := st.GetAliyunImage(archName, config.Platform.AlibabaCloud.Region)
		if err != nil {
			return "", err
		}
		return osimage, nil
	case powervs.Name:
		// Check for image URL override
		if config.Platform.PowerVS.ClusterOSImage != "" {
			return config.Platform.PowerVS.ClusterOSImage, nil
		}

		if streamArch.Images.PowerVS != nil {
			vpcRegion := powervs.Regions[config.Platform.PowerVS.Region].VPCRegion
			img := streamArch.Images.PowerVS.Regions[vpcRegion]
			logrus.Debug("Power VS using image ", img.Object)
			return fmt.Sprintf("%s/%s", img.Bucket, img.Object), nil
		}

		return "", fmt.Errorf("%s: No Power VS build found", st.FormatPrefix(archName))
	case none.Name:
		return "", nil
	case nutanix.Name:
		if config.Platform.Nutanix != nil && config.Platform.Nutanix.ClusterOSImage != "" {
			return config.Platform.Nutanix.ClusterOSImage, nil
		}
		if a, ok := streamArch.Artifacts["nutanix"]; ok {
			return rhcos.FindArtifactURL(a)
		}
		return "", fmt.Errorf("%s: No nutanix build found", st.FormatPrefix(archName))
	default:
		return "", fmt.Errorf("invalid platform %v", config.Platform.Name())
	}
}

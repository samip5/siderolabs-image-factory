// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package profile implements handling of Talos profiles.
package profile

import (
	"strings"

	"github.com/siderolabs/gen/xerrors"
	"github.com/siderolabs/go-pointer"
	"github.com/siderolabs/talos/pkg/imager/profile"
	"github.com/siderolabs/talos/pkg/machinery/constants"

	"github.com/siderolabs/image-service/internal/artifacts"
	"github.com/siderolabs/image-service/pkg/configuration"
)

// InvalidErrorTag tags errors related to invalid profiles.
type InvalidErrorTag struct{}

// parsePlatformArch parses platform-arch string into the profile.
//
// Supported formats:
// - metal-amd64
// - aws-arm64-secureboot
// - metal-rpi_generic-arm64.
func parsePlatformArch(s string, prof *profile.Profile) error {
	s, ok := strings.CutSuffix(s, "-secureboot")
	if ok {
		prof.SecureBoot = pointer.To(true)
	}

	platform, rest, ok := strings.Cut(s, "-")
	if !ok {
		return xerrors.NewTaggedf[InvalidErrorTag]("invalid platform-arch: %q", s)
	}

	prof.Platform = platform

	if platform == constants.PlatformMetal && strings.HasSuffix(rest, "-"+string(artifacts.ArchArm64)) {
		// arm64 metal images might be "board" images
		prof.Board, rest, _ = strings.Cut(rest, "-")
	}

	return parseArch(rest, prof)
}

func parseArch(s string, prof *profile.Profile) error {
	switch artifacts.Arch(s) {
	case artifacts.ArchAmd64, artifacts.ArchArm64:
		prof.Arch = s

		return nil
	default:
		return xerrors.NewTaggedf[InvalidErrorTag]("invalid architecture: %q", s)
	}
}

// ParseFromPath parses imager profile from the file path.
//
//nolint:gocognit,gocyclo,cyclop
func ParseFromPath(path string) (profile.Profile, error) {
	var prof profile.Profile

	// kernel-<arch>
	if rest, ok := strings.CutPrefix(path, "kernel-"); ok {
		prof.Output.Kind = profile.OutKindKernel
		prof.Output.OutFormat = profile.OutFormatRaw
		prof.Platform = constants.PlatformMetal // doesn't matter for kernel output

		if err := parseArch(rest, &prof); err != nil {
			return prof, err
		}

		return prof, nil
	}

	// cmdline-<platform>-<arch>
	if rest, ok := strings.CutPrefix(path, "cmdline-"); ok {
		prof.Output.Kind = profile.OutKindCmdline
		prof.Output.OutFormat = profile.OutFormatRaw

		if err := parsePlatformArch(rest, &prof); err != nil {
			return prof, err
		}

		return prof, nil
	}

	// initramfs-<arch>.xz
	if rest, ok := strings.CutPrefix(path, "initramfs-"); ok {
		if rest, ok = strings.CutSuffix(rest, ".xz"); ok {
			prof.Output.Kind = profile.OutKindInitramfs
			prof.Output.OutFormat = profile.OutFormatRaw
			prof.Platform = constants.PlatformMetal // doesn't matter for initramfs output

			if err := parseArch(rest, &prof); err != nil {
				return prof, err
			}

			return prof, nil
		}
	}

	// <platform>-<arch>.iso
	if rest, ok := strings.CutSuffix(path, ".iso"); ok {
		prof.Output.Kind = profile.OutKindISO
		prof.Output.OutFormat = profile.OutFormatRaw

		if err := parsePlatformArch(rest, &prof); err != nil {
			return prof, err
		}

		return prof, nil
	}

	// <platform>-<arch>-secureboot-uki.efi
	if rest, ok := strings.CutSuffix(path, "-uki.efi"); ok {
		prof.Output.Kind = profile.OutKindUKI
		prof.Output.OutFormat = profile.OutFormatRaw

		if err := parsePlatformArch(rest, &prof); err != nil {
			return prof, err
		}

		return prof, nil
	}

	// installer-<arch>[-secureboot].tar
	if rest, ok := strings.CutPrefix(path, "installer-"); ok {
		if rest, ok = strings.CutSuffix(rest, ".tar"); ok {
			prof.Output.Kind = profile.OutKindInstaller
			prof.Output.OutFormat = profile.OutFormatRaw
			prof.Platform = constants.PlatformMetal // doesn't matter for installer output

			rest, ok = strings.CutSuffix(rest, "-secureboot")
			if ok {
				prof.SecureBoot = pointer.To(true)
			}

			if err := parseArch(rest, &prof); err != nil {
				return prof, err
			}

			return prof, nil
		}
	}

	// at this point, we assume that the path is a disk image, so we start parsing it from the end, cutting the output format suffixes
	prof.Output.Kind = profile.OutKindImage
	prof.Output.ImageOptions = &profile.ImageOptions{
		DiskSize: profile.DefaultRAWDiskSize,
	}

	// first, cut output format: .tar.gz, .gz, .xz (otherwise it's raw uncompressed)
	prof.Output.OutFormat = profile.OutFormatRaw

	for _, outFormat := range []profile.OutFormat{
		profile.OutFormatTar,
		profile.OutFormatGZ,
		profile.OutFormatXZ,
	} {
		var ok bool

		if path, ok = strings.CutSuffix(path, outFormat.String()); ok {
			prof.Output.OutFormat = outFormat

			break
		}
	}

	// second, figure out the disk format
	for _, diskFormat := range []profile.DiskFormat{
		profile.DiskFormatRaw,
		profile.DiskFormatQCOW2,
		profile.DiskFormatVPC,
		profile.DiskFormatOVA,
	} {
		var ok bool

		if path, ok = strings.CutSuffix(path, "."+diskFormat.String()); ok {
			prof.Output.ImageOptions.DiskFormat = diskFormat

			break
		}
	}

	if prof.Output.ImageOptions.DiskFormat == profile.DiskFormatUnknown {
		return prof, xerrors.NewTaggedf[InvalidErrorTag]("invalid profile path: %q", path)
	}

	// third, figure out the platform and arch
	if err := parsePlatformArch(path, &prof); err != nil {
		return prof, err
	}

	// last step: pull in the disk format options from the respective default profile (if any)
	if defaultProfile, ok := profile.Default[prof.Platform]; ok {
		if defaultProfile.Output.ImageOptions.DiskSize != 0 {
			prof.Output.ImageOptions.DiskSize = defaultProfile.Output.ImageOptions.DiskSize
		}

		if defaultProfile.Output.ImageOptions.DiskFormatOptions != "" {
			prof.Output.ImageOptions.DiskFormatOptions = defaultProfile.Output.ImageOptions.DiskFormatOptions
		}
	}

	return prof, nil
}

// EnhanceFromConfiguration enhances the profile with the configuration.
func EnhanceFromConfiguration(prof profile.Profile, config *configuration.Configuration, versionTag string) (profile.Profile, error) {
	if len(config.Customization.SystemExtensions.OfficialExtensions) > 0 {
		// TODO: implement me
		return prof, xerrors.NewTaggedf[InvalidErrorTag]("system extensions are not supported yet")
	}

	if prof.Output.Kind != profile.OutKindInitramfs && prof.Output.Kind != profile.OutKindKernel && prof.Output.Kind != profile.OutKindInstaller {
		// skip customizations for profile kinds which don't support it
		prof.Customization.ExtraKernelArgs = append(prof.Customization.ExtraKernelArgs, config.Customization.ExtraKernelArgs...)
	}

	prof.Version = versionTag

	return prof, nil
}
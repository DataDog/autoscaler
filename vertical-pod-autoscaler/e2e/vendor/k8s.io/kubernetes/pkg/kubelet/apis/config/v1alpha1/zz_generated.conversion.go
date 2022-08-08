//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Code generated by conversion-gen. DO NOT EDIT.

package v1alpha1

import (
	unsafe "unsafe"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	conversion "k8s.io/apimachinery/pkg/conversion"
	runtime "k8s.io/apimachinery/pkg/runtime"
	v1alpha1 "k8s.io/kubelet/config/v1alpha1"
	config "k8s.io/kubernetes/pkg/kubelet/apis/config"
)

func init() {
	localSchemeBuilder.Register(RegisterConversions)
}

// RegisterConversions adds conversion functions to the given scheme.
// Public to allow building arbitrary schemes.
func RegisterConversions(s *runtime.Scheme) error {
	if err := s.AddGeneratedConversionFunc((*v1alpha1.CredentialProvider)(nil), (*config.CredentialProvider)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_CredentialProvider_To_config_CredentialProvider(a.(*v1alpha1.CredentialProvider), b.(*config.CredentialProvider), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*config.CredentialProvider)(nil), (*v1alpha1.CredentialProvider)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_config_CredentialProvider_To_v1alpha1_CredentialProvider(a.(*config.CredentialProvider), b.(*v1alpha1.CredentialProvider), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.CredentialProviderConfig)(nil), (*config.CredentialProviderConfig)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_CredentialProviderConfig_To_config_CredentialProviderConfig(a.(*v1alpha1.CredentialProviderConfig), b.(*config.CredentialProviderConfig), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*config.CredentialProviderConfig)(nil), (*v1alpha1.CredentialProviderConfig)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_config_CredentialProviderConfig_To_v1alpha1_CredentialProviderConfig(a.(*config.CredentialProviderConfig), b.(*v1alpha1.CredentialProviderConfig), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.ExecEnvVar)(nil), (*config.ExecEnvVar)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_ExecEnvVar_To_config_ExecEnvVar(a.(*v1alpha1.ExecEnvVar), b.(*config.ExecEnvVar), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*config.ExecEnvVar)(nil), (*v1alpha1.ExecEnvVar)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_config_ExecEnvVar_To_v1alpha1_ExecEnvVar(a.(*config.ExecEnvVar), b.(*v1alpha1.ExecEnvVar), scope)
	}); err != nil {
		return err
	}
	return nil
}

func autoConvert_v1alpha1_CredentialProvider_To_config_CredentialProvider(in *v1alpha1.CredentialProvider, out *config.CredentialProvider, s conversion.Scope) error {
	out.Name = in.Name
	out.MatchImages = *(*[]string)(unsafe.Pointer(&in.MatchImages))
	out.DefaultCacheDuration = (*v1.Duration)(unsafe.Pointer(in.DefaultCacheDuration))
	out.APIVersion = in.APIVersion
	out.Args = *(*[]string)(unsafe.Pointer(&in.Args))
	out.Env = *(*[]config.ExecEnvVar)(unsafe.Pointer(&in.Env))
	return nil
}

// Convert_v1alpha1_CredentialProvider_To_config_CredentialProvider is an autogenerated conversion function.
func Convert_v1alpha1_CredentialProvider_To_config_CredentialProvider(in *v1alpha1.CredentialProvider, out *config.CredentialProvider, s conversion.Scope) error {
	return autoConvert_v1alpha1_CredentialProvider_To_config_CredentialProvider(in, out, s)
}

func autoConvert_config_CredentialProvider_To_v1alpha1_CredentialProvider(in *config.CredentialProvider, out *v1alpha1.CredentialProvider, s conversion.Scope) error {
	out.Name = in.Name
	out.MatchImages = *(*[]string)(unsafe.Pointer(&in.MatchImages))
	out.DefaultCacheDuration = (*v1.Duration)(unsafe.Pointer(in.DefaultCacheDuration))
	out.APIVersion = in.APIVersion
	out.Args = *(*[]string)(unsafe.Pointer(&in.Args))
	out.Env = *(*[]v1alpha1.ExecEnvVar)(unsafe.Pointer(&in.Env))
	return nil
}

// Convert_config_CredentialProvider_To_v1alpha1_CredentialProvider is an autogenerated conversion function.
func Convert_config_CredentialProvider_To_v1alpha1_CredentialProvider(in *config.CredentialProvider, out *v1alpha1.CredentialProvider, s conversion.Scope) error {
	return autoConvert_config_CredentialProvider_To_v1alpha1_CredentialProvider(in, out, s)
}

func autoConvert_v1alpha1_CredentialProviderConfig_To_config_CredentialProviderConfig(in *v1alpha1.CredentialProviderConfig, out *config.CredentialProviderConfig, s conversion.Scope) error {
	out.Providers = *(*[]config.CredentialProvider)(unsafe.Pointer(&in.Providers))
	return nil
}

// Convert_v1alpha1_CredentialProviderConfig_To_config_CredentialProviderConfig is an autogenerated conversion function.
func Convert_v1alpha1_CredentialProviderConfig_To_config_CredentialProviderConfig(in *v1alpha1.CredentialProviderConfig, out *config.CredentialProviderConfig, s conversion.Scope) error {
	return autoConvert_v1alpha1_CredentialProviderConfig_To_config_CredentialProviderConfig(in, out, s)
}

func autoConvert_config_CredentialProviderConfig_To_v1alpha1_CredentialProviderConfig(in *config.CredentialProviderConfig, out *v1alpha1.CredentialProviderConfig, s conversion.Scope) error {
	out.Providers = *(*[]v1alpha1.CredentialProvider)(unsafe.Pointer(&in.Providers))
	return nil
}

// Convert_config_CredentialProviderConfig_To_v1alpha1_CredentialProviderConfig is an autogenerated conversion function.
func Convert_config_CredentialProviderConfig_To_v1alpha1_CredentialProviderConfig(in *config.CredentialProviderConfig, out *v1alpha1.CredentialProviderConfig, s conversion.Scope) error {
	return autoConvert_config_CredentialProviderConfig_To_v1alpha1_CredentialProviderConfig(in, out, s)
}

func autoConvert_v1alpha1_ExecEnvVar_To_config_ExecEnvVar(in *v1alpha1.ExecEnvVar, out *config.ExecEnvVar, s conversion.Scope) error {
	out.Name = in.Name
	out.Value = in.Value
	return nil
}

// Convert_v1alpha1_ExecEnvVar_To_config_ExecEnvVar is an autogenerated conversion function.
func Convert_v1alpha1_ExecEnvVar_To_config_ExecEnvVar(in *v1alpha1.ExecEnvVar, out *config.ExecEnvVar, s conversion.Scope) error {
	return autoConvert_v1alpha1_ExecEnvVar_To_config_ExecEnvVar(in, out, s)
}

func autoConvert_config_ExecEnvVar_To_v1alpha1_ExecEnvVar(in *config.ExecEnvVar, out *v1alpha1.ExecEnvVar, s conversion.Scope) error {
	out.Name = in.Name
	out.Value = in.Value
	return nil
}

// Convert_config_ExecEnvVar_To_v1alpha1_ExecEnvVar is an autogenerated conversion function.
func Convert_config_ExecEnvVar_To_v1alpha1_ExecEnvVar(in *config.ExecEnvVar, out *v1alpha1.ExecEnvVar, s conversion.Scope) error {
	return autoConvert_config_ExecEnvVar_To_v1alpha1_ExecEnvVar(in, out, s)
}
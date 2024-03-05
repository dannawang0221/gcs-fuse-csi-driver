/*
Copyright 2018 The Kubernetes Authors.
Copyright 2022 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package testsuites

import (
	"context"
	"fmt"
	"strings"

	"github.com/googlecloudplatform/gcs-fuse-csi-driver/test/e2e/specs"
	"github.com/onsi/ginkgo/v2"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/kubernetes/test/e2e/framework"
	e2evolume "k8s.io/kubernetes/test/e2e/framework/volume"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

type gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite struct {
	tsInfo storageframework.TestSuiteInfo
}

// InitGcsFuseCSIGCSFuseIntegrationFileCacheTestSuite returns gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite that implements TestSuite interface.
func InitGcsFuseCSIGCSFuseIntegrationFileCacheTestSuite() storageframework.TestSuite {
	return &gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite{
		tsInfo: storageframework.TestSuiteInfo{
			Name: "gcsfuseIntegrationFileCache",
			TestPatterns: []storageframework.TestPattern{
				storageframework.DefaultFsCSIEphemeralVolume,
			},
		},
	}
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite) GetTestSuiteInfo() storageframework.TestSuiteInfo {
	return t.tsInfo
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite) SkipUnsupportedTests(_ storageframework.TestDriver, _ storageframework.TestPattern) {
}

func (t *gcsFuseCSIGCSFuseIntegrationFileCacheTestSuite) DefineTests(driver storageframework.TestDriver, pattern storageframework.TestPattern) {
	type local struct {
		config         *storageframework.PerTestConfig
		volumeResource *storageframework.VolumeResource
	}
	var l local
	ctx := context.Background()

	// Beware that it also registers an AfterEach which renders f unusable. Any code using
	// f must run inside an It or Context callback.
	f := framework.NewFrameworkWithCustomTimeouts("gcsfuse-integration-file-cache", storageframework.GetDriverTimeouts(driver))
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged

	init := func(configPrefix ...string) {
		l = local{}
		l.config = driver.PrepareTest(ctx, f)
		if len(configPrefix) > 0 {
			l.config.Prefix = configPrefix[0]
		}
		l.volumeResource = storageframework.CreateVolumeResource(ctx, driver, l.config, pattern, e2evolume.SizeRange{})
	}

	cleanup := func() {
		var cleanUpErrs []error
		cleanUpErrs = append(cleanUpErrs, l.volumeResource.CleanupResource(ctx))
		err := utilerrors.NewAggregate(cleanUpErrs)
		framework.ExpectNoError(err, "while cleaning up")
	}

	gcsfuseIntegrationFileCacheTest := func(testName string, readOnly bool, fileCacheCapacity, fileCacheForRangeRead, metadataCacheTTLSeconds string, mountOptions ...string) {
		ginkgo.By("Configuring the test pod")
		tPod := specs.NewTestPod(f.ClientSet, f.Namespace)
		tPod.SetImage(specs.GolangImage)
		tPod.SetCommand("tail -F /tmp/gcsfuse_read_cache_test_logs/log.json")
		tPod.SetResource("1", "1Gi", "5Gi")
		if strings.HasPrefix(testName, "TestRangeReadTest") {
			tPod.SetResource("1", "2Gi", "5Gi")
		}

		l.volumeResource.VolSource.CSI.VolumeAttributes["fileCacheCapacity"] = fileCacheCapacity
		l.volumeResource.VolSource.CSI.VolumeAttributes["fileCacheForRangeRead"] = fileCacheForRangeRead
		l.volumeResource.VolSource.CSI.VolumeAttributes["metadataCacheTTLSeconds"] = metadataCacheTTLSeconds

		tPod.SetupTmpVolumeMount("/tmp/gcsfuse_read_cache_test_logs")
		tPod.SetupCacheVolumeMount("/tmp/cache-dir", ".volumes/"+volumeName)
		mountOptions = append(mountOptions, "logging:file-path:/gcsfuse-tmp/log.json", "logging:format:json")

		tPod.SetupVolume(l.volumeResource, volumeName, mountPath, readOnly, mountOptions...)
		tPod.SetAnnotations(map[string]string{
			"gke-gcsfuse/cpu-limit":               "250m",
			"gke-gcsfuse/memory-limit":            "256Mi",
			"gke-gcsfuse/ephemeral-storage-limit": "2Gi",
		})

		bucketName := l.volumeResource.VolSource.CSI.VolumeAttributes["bucketName"]

		ginkgo.By("Deploying the test pod")
		tPod.Create(ctx)
		defer tPod.Cleanup(ctx)

		ginkgo.By("Checking that the test pod is running")
		tPod.WaitForRunning(ctx)

		ginkgo.By("Checking that the test pod command exits with no error")
		if readOnly {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep ro,", mountPath))
		} else {
			tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, fmt.Sprintf("mount | grep %v | grep rw,", mountPath))
		}

		ginkgo.By("Checking that the gcsfuse integration tests exits with no error")
		tPod.VerifyExecInPodSucceed(f, specs.TesterContainerName, "git clone https://github.com/GoogleCloudPlatform/gcsfuse.git")

		baseTestCommand := fmt.Sprintf("export PATH=$PATH:/usr/local/go/bin && cd %v/read_cache && GODEBUG=asyncpreemptoff=1 go test . -p 1 --integrationTest -v --mountedDirectory=%v --testbucket=%v -run %v", gcsfuseIntegrationTestsBasePath, mountPath, bucketName, testName)
		tPod.VerifyExecInPodSucceedWithFullOutput(f, specs.TesterContainerName, baseTestCommand)
	}

	// The following test cases are derived from https://github.com/GoogleCloudPlatform/gcsfuse/blob/master/tools/integration_tests/run_tests_mounted_directory.sh

	ginkgo.It("should succeed in TestCacheFileForRangeReadFalseTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestCacheFileForRangeReadFalseTest/TestRangeReadsWithCacheMiss", false, "50Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestCacheFileForRangeReadFalseTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestCacheFileForRangeReadFalseTest/TestConcurrentReads_ReadIsTreatedNonSequentialAfterFileIsRemovedFromCache", false, "50Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestCacheFileForRangeReadTrueTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestCacheFileForRangeReadTrueTest/TestRangeReadsWithCacheHit", false, "50Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestDisabledCacheTTLTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestDisabledCacheTTLTest/TestReadAfterObjectUpdateIsCacheMiss", false, "9Mi", "false", "0")
	})

	ginkgo.It("should succeed in TestLocalModificationTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestLocalModificationTest/TestReadAfterLocalGCSFuseWriteIsCacheMiss", false, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsWithinReadChunkSize", false, "500Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithChunkDownloaded", false, "500Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 3", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithoutChunkDownloaded", false, "500Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 4", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsWithinReadChunkSize", false, "500Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 5", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithChunkDownloaded", false, "500Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestRangeReadTest 6", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestRangeReadTest/TestRangeReadsBeyondReadChunkSizeWithoutChunkDownloaded", false, "500Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestSecondSequentialReadIsCacheHit", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileSequentiallyLargerThanCacheCapacity", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 3", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileRandomlyLargerThanCacheCapacity", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 4", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesMoreThanCacheLimit", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 5", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesWithinCacheLimit", true, "9Mi", "false", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 6", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestSecondSequentialReadIsCacheHit", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 7", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileSequentiallyLargerThanCacheCapacity", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 8", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadFileRandomlyLargerThanCacheCapacity", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 9", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesMoreThanCacheLimit", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestReadOnlyTest 10", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestReadOnlyTest/TestReadMultipleFilesWithinCacheLimit", true, "9Mi", "true", "3600")
	})

	ginkgo.It("should succeed in TestSmallCacheTTLTest 1", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestSmallCacheTTLTest/TestReadAfterUpdateAndCacheExpiryIsCacheMiss", false, "9Mi", "false", "10")
	})

	ginkgo.It("should succeed in TestSmallCacheTTLTest 2", func() {
		init()
		defer cleanup()

		gcsfuseIntegrationFileCacheTest("TestSmallCacheTTLTest/TestReadForLowMetaDataCacheTTLIsCacheHit", false, "9Mi", "false", "10")
	})
}

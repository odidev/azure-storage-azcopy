package cmd

import (
	"github.com/Azure/azure-storage-azcopy/common"
	chk "gopkg.in/check.v1"
	"io/ioutil"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultLogVerbosityForSync = "WARNING"
	defaultOutputFormatForSync = "text"
)

func runSyncAndVerify(c *chk.C, raw rawSyncCmdArgs, verifier func(err error)) {
	// the simulated user input should parse properly
	cooked, err := raw.cook()
	c.Assert(err, chk.IsNil)

	// the enumeration ends when process() returns
	err = cooked.process()

	// the err is passed to verified, which knows whether it is expected or not
	verifier(err)
}

func validateTransfersAreScheduled(c *chk.C, srcDirName, dstDirName string, expectedTransfers []string, mockedRPC interceptor) {
	// validate that the right number of transfers were scheduled
	c.Assert(len(mockedRPC.transfers), chk.Equals, len(expectedTransfers))

	// validate that the right transfers were sent
	lookupMap := scenarioHelper{}.convertListToMap(expectedTransfers)
	for _, transfer := range mockedRPC.transfers {
		srcRelativeFilePath := strings.Replace(transfer.Source, srcDirName+common.AZCOPY_PATH_SEPARATOR_STRING, "", 1)
		dstRelativeFilePath := strings.Replace(transfer.Destination, dstDirName+common.AZCOPY_PATH_SEPARATOR_STRING, "", 1)

		// the relative paths should be equal
		c.Assert(srcRelativeFilePath, chk.Equals, dstRelativeFilePath)

		// look up the source from the expected transfers, make sure it exists
		_, srcExist := lookupMap[dstRelativeFilePath]
		c.Assert(srcExist, chk.Equals, true)

		// look up the destination from the expected transfers, make sure it exists
		_, dstExist := lookupMap[dstRelativeFilePath]
		c.Assert(dstExist, chk.Equals, true)
	}
}

func getDefaultRawInput(src, dst string) rawSyncCmdArgs {
	return rawSyncCmdArgs{
		src:                 src,
		dst:                 dst,
		recursive:           true,
		logVerbosity:        defaultLogVerbosityForSync,
		output:              defaultOutputFormatForSync,
		force:               true,
		md5ValidationOption: common.DefaultHashValidationOption.String(),
	}
}

// regular blob->file sync
func (s *cmdIntegrationSuite) TestSyncDownloadWithSingleFile(c *chk.C) {
	bsu := getBSU()

	// set up the container with a single blob
	blobName := "singleblobisbest"
	blobList := []string{blobName}
	containerURL, containerName := createNewContainer(c, bsu)
	scenarioHelper{}.generateBlobs(c, containerURL, blobList)
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)

	// set up the destination as a single file
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)
	dstFileName := blobName
	scenarioHelper{}.generateFilesFromList(c, dstDirName, blobList)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawBlobURLWithSAS := scenarioHelper{}.getRawBlobURLWithSAS(c, containerName, blobList[0])
	raw := getDefaultRawInput(rawBlobURLWithSAS.String(), filepath.Join(dstDirName, dstFileName))

	// the file was created after the blob, so no sync should happen
	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.NotNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})

	// sleep for 1 sec so that the blob's last modified times are guaranteed to be newer
	time.Sleep(time.Second)

	// recreate the blob to have a later last modified time
	scenarioHelper{}.generateBlobs(c, containerURL, blobList)
	mockedRPC.reset()

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)

		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, blobList, mockedRPC)
	})
}

// regular container->directory sync but destination is empty, so everything has to be transferred
func (s *cmdIntegrationSuite) TestSyncDownloadWithEmptyDestination(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// set up the destination with an empty folder
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, len(blobList))

		// validate that the right transfers were sent
		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, blobList, mockedRPC)
	})

	// turn off recursive, this time only top blobs should be transferred
	raw.recursive = false
	mockedRPC.reset()

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)
		c.Assert(len(mockedRPC.transfers), chk.Not(chk.Equals), len(blobList))

		for _, transfer := range mockedRPC.transfers {
			localRelativeFilePath := strings.Replace(transfer.Destination, dstDirName+common.AZCOPY_PATH_SEPARATOR_STRING, "", 1)
			c.Assert(strings.Contains(localRelativeFilePath, common.AZCOPY_PATH_SEPARATOR_STRING), chk.Equals, false)
		}
	})
}

// regular container->directory sync but destination is identical to the source, transfers are scheduled based on lmt
func (s *cmdIntegrationSuite) TestSyncDownloadWithIdenticalDestination(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// set up the destination with a folder that have the exact same files
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)
	scenarioHelper{}.generateFilesFromList(c, dstDirName, blobList)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.NotNil)
		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})

	// wait for 1 second so that the last modified times of the blobs are guaranteed to be newer
	time.Sleep(time.Second)

	// refresh the blobs' last modified time so that they are newer
	scenarioHelper{}.generateBlobs(c, containerURL, blobList)
	mockedRPC.reset()

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)
		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, blobList, mockedRPC)
	})
}

// regular container->directory sync where destination is missing some files from source, and also has some extra files
func (s *cmdIntegrationSuite) TestSyncDownloadWithMismatchedDestination(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// set up the destination with a folder that have half of the files from source
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)
	scenarioHelper{}.generateFilesFromList(c, dstDirName, blobList[0:len(blobList)/2])
	scenarioHelper{}.generateFilesFromList(c, dstDirName, []string{"extraFile1.pdf, extraFile2.txt"})
	expectedOutput := blobList[len(blobList)/2:] // the missing half of source files should be transferred

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)
		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, expectedOutput, mockedRPC)

		// make sure the extra files were deleted
		currentDstFileList, err := ioutil.ReadDir(dstDirName)
		extraFilesFound := false
		for _, file := range currentDstFileList {
			if strings.Contains(file.Name(), "extra") {
				extraFilesFound = true
			}
		}

		c.Assert(extraFilesFound, chk.Equals, false)
	})
}

// include flag limits the scope of source/destination comparison
func (s *cmdIntegrationSuite) TestSyncDownloadWithIncludeFlag(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// add special blobs that we wish to include
	blobsToInclude := []string{"important.pdf", "includeSub/amazing.jpeg", "exactName"}
	scenarioHelper{}.generateBlobs(c, containerURL, blobsToInclude)
	includeString := "*.pdf;*.jpeg;exactName"

	// set up the destination with an empty folder
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)
	raw.include = includeString

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)
		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, blobsToInclude, mockedRPC)
	})
}

// exclude flag limits the scope of source/destination comparison
func (s *cmdIntegrationSuite) TestSyncDownloadWithExcludeFlag(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// add special blobs that we wish to exclude
	blobsToExclude := []string{"notGood.pdf", "excludeSub/lame.jpeg", "exactName"}
	scenarioHelper{}.generateBlobs(c, containerURL, blobsToExclude)
	excludeString := "*.pdf;*.jpeg;exactName"

	// set up the destination with an empty folder
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)
	raw.exclude = excludeString

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)
		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, blobList, mockedRPC)
	})
}

// include and exclude flag can work together to limit the scope of source/destination comparison
func (s *cmdIntegrationSuite) TestSyncDownloadWithIncludeAndExcludeFlag(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// add special blobs that we wish to include
	blobsToInclude := []string{"important.pdf", "includeSub/amazing.jpeg"}
	scenarioHelper{}.generateBlobs(c, containerURL, blobsToInclude)
	includeString := "*.pdf;*.jpeg;exactName"

	// add special blobs that we wish to exclude
	// note that the excluded files also match the include string
	blobsToExclude := []string{"sorry.pdf", "exclude/notGood.jpeg", "exactName", "sub/exactName"}
	scenarioHelper{}.generateBlobs(c, containerURL, blobsToExclude)
	excludeString := "so*;not*;exactName"

	// set up the destination with an empty folder
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)
	raw.include = includeString
	raw.exclude = excludeString

	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.IsNil)
		validateTransfersAreScheduled(c, containerURL.String(), dstDirName, blobsToInclude, mockedRPC)
	})
}

// validate the bug fix for this scenario
func (s *cmdIntegrationSuite) TestSyncDownloadWithMissingDestination(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// set up the destination as a missing folder
	dstDirName := filepath.Join(scenarioHelper{}.generateLocalDirectory(c), "imbatman")

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), dstDirName)

	runSyncAndVerify(c, raw, func(err error) {
		// error should not be nil, but the app should not crash either
		c.Assert(err, chk.NotNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})
}

// there is a type mismatch between the source and destination
func (s *cmdIntegrationSuite) TestSyncMismatchContainerAndFile(c *chk.C) {
	bsu := getBSU()

	// set up the container with numerous blobs
	containerURL, containerName := createNewContainer(c, bsu)
	blobList := scenarioHelper{}.generateCommonRemoteScenario(c, containerURL, "")
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)
	c.Assert(len(blobList), chk.Not(chk.Equals), 0)

	// set up the destination as a single file
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)
	dstFileName := blobList[0]
	scenarioHelper{}.generateFilesFromList(c, dstDirName, blobList)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawContainerURLWithSAS := scenarioHelper{}.getRawContainerURLWithSAS(c, containerName)
	raw := getDefaultRawInput(rawContainerURLWithSAS.String(), filepath.Join(dstDirName, dstFileName))

	// type mismatch, we should get an error
	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.NotNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})

	// reverse the source and destination
	raw = getDefaultRawInput(filepath.Join(dstDirName, dstFileName), rawContainerURLWithSAS.String())

	// type mismatch, we should get an error
	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.NotNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})
}

// there is a type mismatch between the source and destination
func (s *cmdIntegrationSuite) TestSyncMismatchBlobAndDirectory(c *chk.C) {
	bsu := getBSU()

	// set up the container with a single blob
	blobName := "singleblobisbest"
	blobList := []string{blobName}
	containerURL, containerName := createNewContainer(c, bsu)
	scenarioHelper{}.generateBlobs(c, containerURL, blobList)
	defer deleteContainer(c, containerURL)
	c.Assert(containerURL, chk.NotNil)

	// set up the destination as a directory
	dstDirName := scenarioHelper{}.generateLocalDirectory(c)

	// set up interceptor
	mockedRPC := interceptor{}
	Rpc = mockedRPC.intercept
	mockedRPC.init()

	// construct the raw input to simulate user input
	rawBlobURLWithSAS := scenarioHelper{}.getRawBlobURLWithSAS(c, containerName, blobList[0])
	raw := getDefaultRawInput(rawBlobURLWithSAS.String(), dstDirName)

	// type mismatch, we should get an error
	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.NotNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})

	// reverse the source and destination
	raw = getDefaultRawInput(dstDirName, rawBlobURLWithSAS.String())

	// type mismatch, we should get an error
	runSyncAndVerify(c, raw, func(err error) {
		c.Assert(err, chk.NotNil)

		// validate that the right number of transfers were scheduled
		c.Assert(len(mockedRPC.transfers), chk.Equals, 0)
	})
}

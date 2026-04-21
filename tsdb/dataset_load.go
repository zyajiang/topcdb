package tsdb

import (
	"bufio"
	"fmt"
	"io/fs"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
)

const timeDelta = 1000

type lb struct {
	Labels labels.Labels
	Ref    *storage.SeriesRef
}

type TVPair struct {
	T int64
	V float64
}

type DatasetLoader struct {
	Samples [][]TVPair
	Prec    []float64

	Scrape []*lb
}

type oooInput struct {
	Arrive  []int64
	Samples []TVPair
}

func (o *oooInput) Len() int {
	return len(o.Arrive)
}

func (o *oooInput) Less(i, j int) bool {
	return o.Arrive[i] < o.Arrive[j]
}

func (o *oooInput) Swap(i, j int) {
	o.Arrive[i], o.Arrive[j] = o.Arrive[j], o.Arrive[i]
	o.Samples[i], o.Samples[j] = o.Samples[j], o.Samples[i]
}

func (b *DatasetLoader) GenerateOOO() {
	source := rand.NewSource(0)
	r := rand.New(source)
	oooRatio := float64(0.2)
	for i := 0; i < len(b.Samples); i += 1 {
		Arrive := make([]int64, len(b.Samples[i]))
		for j := 0; j < len(b.Samples[i]); j += 1 {
			coin := r.Float64()
			Arrive[j] = b.Samples[i][j].T
			if coin < oooRatio {
				tDelta := int64((1200 * timeDelta * coin / oooRatio))
				Arrive[j] = b.Samples[i][j].T + tDelta
			}
		}
		sort.Sort(&oooInput{Arrive: Arrive, Samples: b.Samples[i]})
	}
}

func NewDatasetLoader() *DatasetLoader {
	return &DatasetLoader{
		Samples: make([][]TVPair, 0, 8),
		Prec:    make([]float64, 0, 8),
		Scrape:  make([]*lb, 0, 8),
	}
}

func (b *DatasetLoader) ReadDataset(dataset, path string) error {
	num_labels := 1

	switch dataset {
	case "electricity":
		if err := b.ReadElectricityFile(filepath.Join(path, "LD2011_2014.txt")); err != nil {
			return err
		}

		num_labels = len(b.Samples)
	case "geolife":
		err := filepath.WalkDir(path, func(subpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".plt") {
				if err := b.ReadGeolifeFile(subpath); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			panic(err)
		}

		num_labels = 5
	case "wisdm":
		err := filepath.WalkDir(path, func(subpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".txt") {
				if err := b.ReadWISDMFile(subpath); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			panic(err)
		}

		num_labels = 3
	case "pamapv2":
		// list all files in the directory
		listFiles := func(dir string) ([]string, error) {
			files := []string{}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".dat") {
					files = append(files, filepath.Join(dir, entry.Name()))
				}
			}
			return files, nil
		}

		for _, sub := range []string{"Protocol", "Optional"} {
			files, err := listFiles(filepath.Join(path, sub))
			if err != nil {
				return err
			}
			for _, file := range files {
				if err := b.ReadPAMAP2File(file); err != nil {
					return err
				}
			}
		}

		num_labels = 39
	case "uci_gas":
		for _, sub := range []string{"ethylene_CO.txt", "ethylene_methane.txt"} {
			if err := b.ReadUCI_GASFile(filepath.Join(path, sub)); err != nil {
				return err
			}
		}

		num_labels = 16
	case "ucr":
		DIR, err := os.Open(path)
		if err != nil {
			log.Fatal(err)
		}
		subdirs, err := DIR.ReadDir(-1)
		DIR.Close()
		if err != nil {
			log.Fatal(err)
		}

		re, err := regexp.Compile(".*tsv")
		if err != nil {
			log.Fatal(err)
		}

		for _, subdir := range subdirs {
			dir, err := os.Open(filepath.Join(path, subdir.Name()))
			if err != nil {
				log.Fatal(err)
			}
			files, err := dir.ReadDir(-1)
			dir.Close()
			if err != nil {
				log.Fatal(err)
			}
			for _, file := range files {
				matched := re.MatchString(file.Name())
				if !matched {
					continue
				}
				if b.ReadUCRFile(filepath.Join(path, subdir.Name(), file.Name())) != nil {
					return err
				}
			}
		}

		num_labels = len(b.Samples)
	case "ett":
		for _, sub := range []string{"ETTh1.csv", "ETTh2.csv", "ETTm1.csv", "ETTm2.csv"} {
			if err := b.ReadETTFile(filepath.Join(path, sub)); err != nil {
				return err
			}
		}

		num_labels = 7
	case "household_voltage":
		if err := b.ReadHouseholdVoltageFile(filepath.Join(path, "household_power_consumption.txt")); err != nil {
			return err
		}

		num_labels = len(b.Samples)
	}

	for i := 0; i < len(b.Samples); i += 1 {
		b.Scrape = append(b.Scrape, &lb{
			Labels: labels.New(
				labels.Label{Name: labels.MetricName, Value: dataset},
				labels.Label{Name: "FileID", Value: strconv.Itoa(i / num_labels)},
				labels.Label{Name: "CaseID", Value: strconv.Itoa(i)},
			),
		})
		// b.Prec[i] = math.Pow(10, -b.Prec[i])
	}

	num_labels += 1
	return nil
}

func (b *DatasetLoader) ReadElectricityFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lbs := len(b.Samples)
	for j := 0; j < 370; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
	}

	row := 0
	data_error := 0

	scanner.Scan()
	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), ";")
		for k := 1; k <= 370; k++ {
			vstring := strings.Replace(line[k], ",", ".", 1)
			val, err := strconv.ParseFloat(vstring, 64)
			if err != nil {
				data_error++
				continue
			}
			b.Samples[lbs+k-1] = append(b.Samples[lbs+k-1], TVPair{int64(row * timeDelta), val})
		}
	}
	fmt.Printf("ReadElectricityFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadGeolifeFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lbs := len(b.Samples)
	for j := 0; j < 5; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
	}

	row := 0
	data_error := 0

	// skip header
	for j := 0; j < 6; j++ {
		scanner.Scan()
	}

	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), ",")
		k := 4 // only read altitude
		// for k := 0; k < 5; k++ {
		val, err := strconv.ParseFloat(line[k], 64)
		if err != nil {
			data_error++
			continue
		}
		b.Samples[lbs+k] = append(b.Samples[lbs+k], TVPair{int64(row * timeDelta), val})
		// }
	}

	fmt.Printf("ReadGeolifeFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadWISDMFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lbs := len(b.Samples)
	for j := 0; j < 3; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
	}

	row := 0
	data_error := 0

	for scanner.Scan() {
		row++
		text := scanner.Text()
		line := strings.Split(text[:len(text)-1], ",")
		for k := 3; k <= 5; k++ {
			val, err := strconv.ParseFloat(line[k], 64)
			if err != nil {
				data_error++
				continue
			}
			b.Samples[lbs+k-3] = append(b.Samples[lbs+k-3], TVPair{int64(row * timeDelta), val})
		}
	}

	fmt.Printf("ReadWISDMFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadHouseholdVoltageFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lbs := len(b.Samples)
	for j := 0; j < 7; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
		b.Prec = append(b.Prec, 0.0)
	}

	row := 0
	val := float64(0)
	data_error := 0

	scanner.Scan()
	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), ";")
		k := 4 // Only read voltage measurement
		// for k := 2; k <= 8; k++ {
		val, err = strconv.ParseFloat(line[k], 64)
		if err != nil {
			data_error++
			continue
		}
		Prec := getDecimalPrecision(line[k])
		if Prec != -1 {
			b.Prec[lbs+k-2] = math.Max(b.Prec[lbs+k-2], float64(Prec))
		}
		b.Samples[lbs+k-2] = append(b.Samples[lbs+k-2], TVPair{int64(row * timeDelta), val})
		// }
	}

	fmt.Printf("ReadPowerConsumptionFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadETTFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(f)
	lbs := len(b.Samples)
	for j := 0; j < 7; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
		b.Prec = append(b.Prec, 0.0)
	}

	row := 0
	val := float64(0)
	data_error := 0

	scanner.Scan()
	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), ",")
		for k := 1; k <= 7; k++ {
			val, err = strconv.ParseFloat(line[k], 64)
			if err != nil {
				data_error++
				continue
			}
			Prec := getDecimalPrecision(line[k])
			if Prec != -1 {
				b.Prec[lbs+k-1] = math.Max(b.Prec[lbs+k-1], float64(Prec))
			}
			b.Samples[lbs+k-1] = append(b.Samples[lbs+k-1], TVPair{int64(row * timeDelta), val})
		}
	}

	fmt.Printf("ReadETTFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadUCI_GASFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(f)
	lbs := len(b.Samples)
	for j := 0; j < 16; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
		b.Prec = append(b.Prec, 0.0)
	}

	row := 0
	data_error := 0

	scanner.Scan()
	for scanner.Scan() {
		row++
		line := strings.Fields(scanner.Text())
		for j := 3; j < len(line); j++ {
			val, err := strconv.ParseFloat(line[j], 64)
			if err != nil {
				data_error++
				continue
			}
			Prec := getDecimalPrecision(line[j])
			if Prec != -1 {
				b.Prec[lbs+j-3] = math.Max(b.Prec[lbs+j-3], float64(Prec))
			}
			b.Samples[lbs+j-3] = append(b.Samples[lbs+j-3], TVPair{int64(row * timeDelta), val})
		}
	}

	fmt.Printf("ReadUCI_GASFile:: data convertion error: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadUCRFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(f)
	row := len(b.Samples)
	data_error := 0

	for scanner.Scan() {
		line := strings.Split(scanner.Text(), "\t")
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
		b.Prec = append(b.Prec, 0.0)

		for j := 0; j < len(line); j++ {
			val, err := strconv.ParseFloat(line[j], 64)
			if err != nil {
				data_error++
				continue
			}
			Prec := getDecimalPrecision(line[j])
			if Prec != -1 {
				b.Prec[row] = math.Max(b.Prec[row], float64(Prec))
			}
			b.Samples[row] = append(b.Samples[row], TVPair{int64(j * timeDelta), val})
		}
		row++
	}
	fmt.Printf("ReadUCRFile:: data convertion error: %d\n", data_error)
	return nil
}

func (b *DatasetLoader) ReadPAMAP2File(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for j := 0; j < 39; j++ {
		b.Samples = append(b.Samples, make([]TVPair, 0, 8))
		b.Prec = append(b.Prec, 0.0)
	}

	row, data_error := 0, 0
	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), " ")
		itr := len(b.Samples) - 39
		for k := 0; k < 3; k++ {
			for j := 3 + 17*k; j <= 15+17*k; j++ {
				val, err := strconv.ParseFloat(line[j], 64)
				if err != nil || line[j] == "NaN" {
					data_error++
					continue
				}
				Prec := getDecimalPrecision(line[j])
				if Prec != -1 {
					b.Prec[itr] = math.Max(b.Prec[itr], float64(Prec))
				}
				b.Samples[itr] = append(b.Samples[itr], TVPair{int64(row * timeDelta), val})
				itr++
			}
		}
	}

	fmt.Printf("ReadPAMAP2File:: data convertion error: %d\n", data_error)
	return nil
}

func getDecimalPrecision(f string) int {
	off := 0
	if len(f) > 0 && (f[0] == '+' || f[0] == '-') {
		off = 1
		if off >= len(f) {
			return -1
		}
	}

	dotIdx := strings.IndexByte(f[off:], '.')
	if dotIdx == -1 {
		return 0
	}
	dotIdx += off

	if dotIdx+1 >= len(f) {
		return 0
	}

	afterDot := f[dotIdx+1:]
	expIdx := strings.IndexAny(afterDot, "eE")

	PrecEnd := len(afterDot)
	if expIdx != -1 {
		PrecEnd = expIdx
	}

	for i := 0; i < PrecEnd; i++ {
		if afterDot[i] < '0' || afterDot[i] > '9' {
			return -1
		}
	}

	return PrecEnd
}

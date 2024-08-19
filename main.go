package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"runtime"
	"strings"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
	"tinygo.org/x/bluetooth"
)

type MQQTConfig struct {
	Topic  string
	Guage  string
	Broker string
	Port   int
}

const (
	BLUETOOTH_ADDRESS             = "A4:C1:37:42:6C:4D"
	METER_NAME                    = "bms"
	BMS_SERVICE_UUID              = "0000ff00-0000-1000-8000-00805f9b34fb"
	BMS_SERVICE_CHARACTERISTIC_TX = "0000ff02-0000-1000-8000-00805f9b34fb"
	BMS_SERVICE_CHARACTERISTIC_RX = "0000ff01-0000-1000-8000-00805f9b34fb"
)

var (
	adapter = bluetooth.DefaultAdapter

	bmsServiceUUID, _ = bluetooth.ParseUUID(BMS_SERVICE_UUID)

	bmsCharacteristicTX, _ = bluetooth.ParseUUID(BMS_SERVICE_CHARACTERISTIC_TX)
	bmsCharacteristicRX, _ = bluetooth.ParseUUID(BMS_SERVICE_CHARACTERISTIC_RX)

	ticker = 0
)

var mqqtConfig = MQQTConfig{
	Topic:  "data/bms",
	Guage:  "data/bms/guage",
	Broker: "127.0.0.1",
	Port:   1883,
}

func main() {
	// Connect to Bluetooth Device
	adapter := initializeBluetoothAdapter()

	device := findAndConnectBMSDevice(adapter)

	discoverCharacteristicAndNotify(device)

}

func initializeBluetoothAdapter() *bluetooth.Adapter {
	err := adapter.Enable()

	if err != nil {
		log.Fatalf("Failed to enable BLE adapter: %v", err)
	}

	return adapter
}

func findAndConnectBMSDevice(adapter *bluetooth.Adapter) *bluetooth.Device {
	ch := make(chan bluetooth.ScanResult, 1)
	go scanForDevice(adapter, ch)

	select {
	case result := <-ch:
		device, err := adapter.Connect(result.Address, bluetooth.ConnectionParams{})
		if err != nil {
			log.Fatalf("Failed to connect to device: %v", err)
		}

		log.Println("Connected to:", result.Address.String(), result.LocalName())
		return &device
	}

}

func scanForDevice(adapter *bluetooth.Adapter, ch chan bluetooth.ScanResult) {
	log.Println("Scanning for BLE Devices...")

	err := adapter.Scan(func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) {
		log.Println(device.Address.String(), device.LocalName())

		if isTargetDevice(device) {
			adapter.StopScan()
			ch <- device
		}
	})

	if err != nil {
		log.Fatalf("Failed to start scanning: %v", err)
	}
}

func isTargetDevice(device bluetooth.ScanResult) bool {
	if runtime.GOOS == "darwin" {
		// uid, _ := bluetooth.ParseUUID(BLUETOOTH_ADDRESS_UUID)
		// return device.Address.UUID == uid
	}
	return device.Address.String() == BLUETOOTH_ADDRESS
}

func discoverCharacteristicAndNotify(device *bluetooth.Device) {
	services, err := device.DiscoverServices([]bluetooth.UUID{bmsServiceUUID})

	if err != nil {
		log.Fatalf("Failed to discover services: %v", err)
	}

	service := services[0]

	log.Println("Service: ", service.String())

	chars, err := service.DiscoverCharacteristics([]bluetooth.UUID{bmsCharacteristicTX, bmsCharacteristicRX})

	if err != nil {
		log.Fatalf("Failed to discover characteristics: %v", err)
	}

	charTX := chars[0]
	charRX := chars[1]

	log.Println("Characteristic TX: ", charTX.String())
	log.Println("Characteristic RX: ", charRX.String())

	// Start reading and writing data
	readAndWriteCharacteristicData(&charTX, &charRX)
}

func readAndWriteCharacteristicData(charTX *bluetooth.DeviceCharacteristic, charRX *bluetooth.DeviceCharacteristic) {
	// All battery pack data
	allData := []byte{0xdd, 0xa5, 0x3, 0x0, 0xff, 0xfd, 0x77}
	// Individual cell data
	cellData := []byte{0xdd, 0xa5, 0x4, 0x0, 0xff, 0xfc, 0x77}

	// Enable notifications on the RX characteristic
	err := charRX.EnableNotifications(bmsNotifyCallback)
	if err != nil {
		log.Fatalf("Failed to enable notifications: %v", err)
	}

	// Continuously write data and read notifications
	for {
		ticker++
		log.Println("Ticker: ", ticker)

		if ticker%2 == 0 {
			_, err = charTX.WriteWithoutResponse(allData)
			if err != nil {
				log.Printf("Failed to write allData: %v", err)
			} else {
				log.Println("Wrote allData to TX characteristic")
			}
		} else {
			_, err = charTX.WriteWithoutResponse(cellData)
			if err != nil {
				log.Printf("Failed to write cellData: %v", err)
			} else {
				log.Println("Wrote cellData to TX characteristic")
			}
		}

		time.Sleep(2 * time.Second)
	}
}

func bmsNotifyCallback(data []byte) {
	log.Println("Received BMS Notification")

	// Convert raw bytes to a hexadecimal string
	hexData := hex.EncodeToString(data)
	textString := hexData

	// Route data based on the content of the hexadecimal string
	switch {
	case strings.Contains(textString, "dd04"): // x04 (1-8 cells)
		cellvolts(data)
	case strings.Contains(textString, "dd03"): // x03
		packInfo(data)
	case strings.Contains(textString, "77") && (len(textString) == 28 || len(textString) == 36): // x03
		log.Println("Cell Info 2")
	}
}

func cellvolts(data []byte) {
	// Unpack the data starting from the 4th byte (skipping the first 4 header bytes)
	celldata := data[4:] // Skip header bytes

	// Create a slice to hold the unpacked cell voltages
	cells1 := make([]uint16, 8)

	// Use binary.Read to unpack the bytes into the cells1 slice
	err := binary.Read(bytes.NewReader(celldata), binary.BigEndian, &cells1)

	if err != nil {
		log.Fatalf("Failed to unpack cell voltages: %v", err)
	}

	// Create and publish the first message
	message := map[string]interface{}{
		"meter": METER_NAME,
		"cell1": float64(cells1[0]) / 1000,
		"cell2": float64(cells1[1]) / 1000,
		"cell3": float64(cells1[2]) / 1000,
		"cell4": float64(cells1[3]) / 1000,
		"cell5": float64(cells1[4]) / 1000,
		"cell6": float64(cells1[5]) / 1000,
		"cell7": float64(cells1[6]) / 1000,
		"cell8": float64(cells1[7]) / 1000,
	}

	publishMessage(message)

	// Calculate min, max, and delta
	cellsmin := min(cells1)
	cellsmax := max(cells1)
	delta := cellsmax - cellsmin
	mincell := indexOf(cells1, cellsmin) + 1
	maxcell := indexOf(cells1, cellsmax) + 1

	// Create and publish the second message
	summaryMessage := map[string]interface{}{
		"meter":    METER_NAME,
		"mincell":  mincell,
		"cellsmin": float64(cellsmin) / 1000,
		"maxcell":  maxcell,
		"cellsmax": float64(cellsmax) / 1000,
		"delta":    float64(delta) / 1000,
	}
	publishMessage(summaryMessage)

	// Print the message map
	log.Printf("Message: %+v\n", message)
	log.Printf("Message: %+v\n", summaryMessage)
}

func packInfo(data []byte) {
	infoData := data[4:] // Skip header bytes

	type BattPackData struct {
		Volts    uint16
		Amps     int16
		Remain   uint16
		Capacity uint16
		Cycles   uint16
		Mdate    uint16
		Balance1 uint16
		Balance2 uint16
	}

	// Create a struct to hold the unpacked data
	var battPackData BattPackData

	// Unpack the data into the struct
	err := binary.Read(bytes.NewReader(infoData), binary.BigEndian, &battPackData)
	if err != nil {
		log.Fatalf("Failed to unpack pack info: %v", err)
	}

	// Convert values to float64 for further calculations
	voltsF := float64(battPackData.Volts) / 100
	ampsF := float64(battPackData.Amps) / 100
	capacityF := float64(battPackData.Capacity) / 100
	remainF := float64(battPackData.Remain) / 100
	wattsF := 24 * ampsF // Calculate watts
	percentage := (remainF / capacityF) * 100

	message := map[string]interface{}{
		"meter":      METER_NAME,
		"volts":      voltsF,
		"amps":       ampsF,
		"watts":      wattsF,
		"remain":     remainF,
		"capacity":   capacityF,
		"cycles":     battPackData.Cycles,
		"percentage": math.Round(percentage*100) / 100,
		"mDate":      battPackData.Mdate,
	}

	publishMessage(message)

	log.Printf("Message: %+v\n", message)

}

func publishMessage(message map[string]interface{}) {
	payload, err := json.Marshal(message)
	if err != nil {
		log.Fatalf("Failed to marshal JSON message: %v", err)
	}

	opts := MQTT.NewClientOptions()

	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", mqqtConfig.Broker, mqqtConfig.Port))

	client := MQTT.NewClient(opts)

	token := client.Connect()

	token.Wait()

	client.Publish(mqqtConfig.Guage, 0, false, payload)

	if token.Error() != nil {
		log.Fatalf("Failed to publish message: %v", token.Error())
	}
}

func min(arr []uint16) uint16 {
	min := arr[0]
	for _, v := range arr {
		if v < min {
			min = v
		}
	}
	return min
}

func max(arr []uint16) uint16 {
	max := arr[0]
	for _, v := range arr {
		if v > max {
			max = v
		}
	}
	return max
}

func indexOf(arr []uint16, value uint16) int {
	for i, v := range arr {
		if v == value {
			return i
		}
	}
	return -1
}

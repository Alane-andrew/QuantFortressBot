// utils/math.go
package utils

import "math"

const Epsilon = 1e-9

// FloatEquals compares two floating-point numbers for near-equality.
func FloatEquals(a, b float64) bool {
	return math.Abs(a-b) < Epsilon
}

// RoundToPrecision rounds a float64 to a specified number of decimal places.
func RoundToPrecision(value float64, precision int) float64 {
	pow := math.Pow(10, float64(precision))
	return math.Round(value*pow) / pow
}

// AdjustPriceToTickSize adjusts a price to conform to the tick size requirement.
// This ensures the price is valid for the specific trading pair.
func AdjustPriceToTickSize(price float64, tickSize float64) float64 {
	if tickSize <= 0 {
		return price
	}
	return math.Round(price/tickSize) * tickSize
}

// TODO: Implement common mathematical calculation functions

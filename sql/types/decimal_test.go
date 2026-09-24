// Copyright 2022 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types

import (
	"context"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dolthub/go-mysql-server/sql"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecimalAccuracy(t *testing.T) {
	t.Skip("This runs 821471 tests, which take quite a while. Re-run this if the max precision is ever updated.")
	precision := 65

	ctx := sql.NewEmptyContext()

	tests := []struct {
		scale     int
		intervals []string
	}{
		{1, []string{"1"}},
		{2, []string{"1"}},
		{3, []string{"1"}},
		{4, []string{"1", "2"}},
		{5, []string{"1", "7", "19"}},
		{6, []string{"1", "17", "173"}},
		{7, []string{"1", "13", "1381"}},
		{8, []string{"1", "127", "15139"}},
		{9, []string{"1", "167", "11311", "157733"}},
		{10, []string{"1", "193", "12119", "1299827"}},
		{11, []string{"1", "1409", "13597", "11566817"}},
		{12, []string{"1", "1462", "162736", "19973059", "153698913"}},
		{13, []string{"1", "17173", "164916", "12810490", "1106465924"}},
		{14, []string{"1", "14145", "1929683", "11237352", "12259001771"}},
		{15, []string{"1", "19702", "1826075", "197350780", "117062654737"}},
		{16, []string{"1", "154259", "1722308", "192079755", "1568355872155"}},
		{17, []string{"1", "199621", "17380400", "189789317", "18535814105416"}},
		{18, []string{"1", "164284", "19555364", "1343158899", "191285386028951"}},
		{19, []string{"1", "1370167", "14327353", "1613296706", "1786126145971438"}},
		{20, []string{"1", "1682382", "156896829", "1502199604", "15400467202762943"}},
		{21, []string{"1", "1908105", "132910114", "17668300548", "145914194398307528"}},
		{22, []string{"1", "11192652", "181987462", "13471431866", "1112655573846229769"}},
		{23, []string{"1", "19628451", "1498686974", "13119001111", "17583200755082903973"}},
		{24, []string{"1", "14855266", "1844358042", "140667369937", "138362583526008386641"}},
		{25, []string{"1", "132605238", "1459826257", "138157739511", "1456272936346618537992"}},
		{26, []string{"1", "178623779", "19310677332", "124692319379", "15924740334465525606269"}},
		{27, []string{"1", "136953077", "13506952725", "1383331590521", "137480986566749829385216"}},
		{28, []string{"1", "1838754847", "16879518108", "1840612305937", "1389868035366355336138689"}},
		{29, []string{"1", "1760427312", "169649694515", "1810557411178", "12907494895459213754558234"}},
		{30, []string{"1", "1823936104", "131352779146", "17050328377892", "146384189585475736836539491"}},
	}

	for _, test := range tests {
		decimalType := MustCreateDecimalType(uint8(precision), uint8(test.scale))
		decimalInt := big.NewInt(0)
		bigIntervals := make([]*big.Int, len(test.intervals))
		for i, interval := range test.intervals {
			bigInterval := new(big.Int)
			_ = bigInterval.UnmarshalText([]byte(interval))
			bigIntervals[i] = bigInterval
		}
		intervalIndex := 0
		baseStr := strings.Repeat("9", precision-test.scale) + "."
		upperBound := new(big.Int)
		_ = upperBound.UnmarshalText([]byte("1" + strings.Repeat("0", test.scale)))

		for decimalInt.Cmp(upperBound) == -1 {
			decimalStr := decimalInt.Text(10)
			fullDecimalStr := strings.Repeat("0", test.scale-len(decimalStr)) + decimalStr
			fullStr := baseStr + fullDecimalStr

			t.Run(fmt.Sprintf("Scale:%v DecVal:%v", test.scale, fullDecimalStr), func(t *testing.T) {
				res, _, err := decimalType.Convert(ctx, fullStr)
				require.NoError(t, err)
				require.Equal(t, fullStr, res.(decimal.Decimal).StringFixed(int32(decimalType.Scale())))
			})

			decimalInt.Add(decimalInt, bigIntervals[intervalIndex])
			intervalIndex = (intervalIndex + 1) % len(bigIntervals)
		}
	}
}

func TestDecimalCompare(t *testing.T) {
	tests := []struct {
		precision   uint8
		scale       uint8
		val1        interface{}
		val2        interface{}
		expectedCmp int
	}{
		{1, 0, nil, 0, 1},
		{1, 0, 0, nil, -1},
		{1, 0, nil, nil, 0},
		{1, 0, "-3.2", 2, -1},
		{1, 1, ".738193", .6948274, 1},
		{5, 0, 0, 1, -1},
		{5, 0, 0, "1", -1},
		{5, 0, "0.23e1", 3, -1},
		{5, 0, "46572e-2", big.NewInt(466), -1},
		{20, 10, "48204.23457e4", 93828432, 1},
		{20, 10, "-.0000000001", 0, -1},
		{20, 10, "-.00000000001", 0, -1},
		{65, 0, "99999999999999999999999999999999999999999999999999999999999999999",
			"99999999999999999999999999999999999999999999999999999999999999998", 1},
		{65, 30, "99999999999999999999999999999999999.999999999999999999999999999998",
			"99999999999999999999999999999999999.999999999999999999999999999999", -1},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%v %v", test.val1, test.val2), func(t *testing.T) {
			cmp, err := MustCreateDecimalType(test.precision, test.scale).Compare(context.Background(), test.val1, test.val2)
			require.NoError(t, err)
			assert.Equal(t, test.expectedCmp, cmp)
		})
	}
}

func TestCreateNonColumnDecimal(t *testing.T) {
	tests := []struct {
		precision    uint8
		scale        uint8
		expectedType DecimalType_
		expectedErr  bool
	}{
		{0, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 10), definesColumn: false, precision: 10, scale: 0}, false},
		{0, 1, DecimalType_{}, true},
		{0, 5, DecimalType_{}, true},
		{0, 10, DecimalType_{}, true},
		{0, 30, DecimalType_{}, true},
		{0, 65, DecimalType_{}, true},
		{0, 66, DecimalType_{}, true},
		{1, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 1), definesColumn: false, precision: 1, scale: 0}, false},
		{1, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: false, precision: 1, scale: 1}, false},
		{1, 5, DecimalType_{}, true},
		{1, 10, DecimalType_{}, true},
		{1, 30, DecimalType_{}, true},
		{1, 65, DecimalType_{}, true},
		{1, 66, DecimalType_{}, true},
		{5, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 5), definesColumn: false, precision: 5, scale: 0}, false},
		{5, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 4), definesColumn: false, precision: 5, scale: 1}, false},
		{5, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: false, precision: 5, scale: 5}, false},
		{5, 10, DecimalType_{}, true},
		{5, 30, DecimalType_{}, true},
		{5, 65, DecimalType_{}, true},
		{5, 66, DecimalType_{}, true},
		{10, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 10), definesColumn: false, precision: 10, scale: 0}, false},
		{10, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 9), definesColumn: false, precision: 10, scale: 1}, false},
		{10, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 5), definesColumn: false, precision: 10, scale: 5}, false},
		{10, 10, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: false, precision: 10, scale: 10}, false},
		{10, 30, DecimalType_{}, true},
		{10, 65, DecimalType_{}, true},
		{10, 66, DecimalType_{}, true},
		{30, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 30), definesColumn: false, precision: 30, scale: 0}, false},
		{30, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 29), definesColumn: false, precision: 30, scale: 1}, false},
		{30, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 25), definesColumn: false, precision: 30, scale: 5}, false},
		{30, 10, DecimalType_{exclusiveUpperBound: decimal.New(1, 20), definesColumn: false, precision: 30, scale: 10}, false},
		{30, 30, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: false, precision: 30, scale: 30}, false},
		{30, 65, DecimalType_{}, true},
		{30, 66, DecimalType_{}, true},
		{65, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 65), definesColumn: false, precision: 65, scale: 0}, false},
		{65, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 64), definesColumn: false, precision: 65, scale: 1}, false},
		{65, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 60), definesColumn: false, precision: 65, scale: 5}, false},
		{65, 10, DecimalType_{exclusiveUpperBound: decimal.New(1, 55), definesColumn: false, precision: 65, scale: 10}, false},
		{65, 30, DecimalType_{exclusiveUpperBound: decimal.New(1, 35), definesColumn: false, precision: 65, scale: 30}, false},
		{65, 65, DecimalType_{}, true},
		{65, 66, DecimalType_{}, true},
		{66, 00, DecimalType_{}, true},
		{66, 01, DecimalType_{}, true},
		{66, 05, DecimalType_{}, true},
		{66, 10, DecimalType_{}, true},
		{66, 30, DecimalType_{}, true},
		{66, 65, DecimalType_{}, true},
		{66, 66, DecimalType_{}, true},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%v %v", test.precision, test.scale), func(t *testing.T) {
			typ, err := CreateDecimalType(test.precision, test.scale)
			if test.expectedErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.expectedType.withBoundsAtScale(), typ)
			}
		})
	}
}

func TestCreateColumnDecimal(t *testing.T) {
	tests := []struct {
		precision    uint8
		scale        uint8
		expectedType DecimalType_
		expectedErr  bool
	}{
		{0, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 10), definesColumn: true, precision: 10, scale: 0}, false},
		{0, 1, DecimalType_{}, true},
		{0, 5, DecimalType_{}, true},
		{0, 10, DecimalType_{}, true},
		{0, 30, DecimalType_{}, true},
		{0, 65, DecimalType_{}, true},
		{0, 66, DecimalType_{}, true},
		{1, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 1), definesColumn: true, precision: 1, scale: 0}, false},
		{1, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: true, precision: 1, scale: 1}, false},
		{1, 5, DecimalType_{}, true},
		{1, 10, DecimalType_{}, true},
		{1, 30, DecimalType_{}, true},
		{1, 65, DecimalType_{}, true},
		{1, 66, DecimalType_{}, true},
		{5, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 5), definesColumn: true, precision: 5, scale: 0}, false},
		{5, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 4), definesColumn: true, precision: 5, scale: 1}, false},
		{5, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: true, precision: 5, scale: 5}, false},
		{5, 10, DecimalType_{}, true},
		{5, 30, DecimalType_{}, true},
		{5, 65, DecimalType_{}, true},
		{5, 66, DecimalType_{}, true},
		{10, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 10), definesColumn: true, precision: 10, scale: 0}, false},
		{10, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 9), definesColumn: true, precision: 10, scale: 1}, false},
		{10, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 5), definesColumn: true, precision: 10, scale: 5}, false},
		{10, 10, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: true, precision: 10, scale: 10}, false},
		{10, 30, DecimalType_{}, true},
		{10, 65, DecimalType_{}, true},
		{10, 66, DecimalType_{}, true},
		{30, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 30), definesColumn: true, precision: 30, scale: 0}, false},
		{30, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 29), definesColumn: true, precision: 30, scale: 1}, false},
		{30, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 25), definesColumn: true, precision: 30, scale: 5}, false},
		{30, 10, DecimalType_{exclusiveUpperBound: decimal.New(1, 20), definesColumn: true, precision: 30, scale: 10}, false},
		{30, 30, DecimalType_{exclusiveUpperBound: decimal.New(1, 0), definesColumn: true, precision: 30, scale: 30}, false},
		{30, 65, DecimalType_{}, true},
		{30, 66, DecimalType_{}, true},
		{65, 0, DecimalType_{exclusiveUpperBound: decimal.New(1, 65), definesColumn: true, precision: 65, scale: 0}, false},
		{65, 1, DecimalType_{exclusiveUpperBound: decimal.New(1, 64), definesColumn: true, precision: 65, scale: 1}, false},
		{65, 5, DecimalType_{exclusiveUpperBound: decimal.New(1, 60), definesColumn: true, precision: 65, scale: 5}, false},
		{65, 10, DecimalType_{exclusiveUpperBound: decimal.New(1, 55), definesColumn: true, precision: 65, scale: 10}, false},
		{65, 30, DecimalType_{exclusiveUpperBound: decimal.New(1, 35), definesColumn: true, precision: 65, scale: 30}, false},
		{65, 65, DecimalType_{}, true},
		{65, 66, DecimalType_{}, true},
		{66, 00, DecimalType_{}, true},
		{66, 01, DecimalType_{}, true},
		{66, 05, DecimalType_{}, true},
		{66, 10, DecimalType_{}, true},
		{66, 30, DecimalType_{}, true},
		{66, 65, DecimalType_{}, true},
		{66, 66, DecimalType_{}, true},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%v %v", test.precision, test.scale), func(t *testing.T) {
			typ, err := CreateColumnDecimalType(test.precision, test.scale)
			if test.expectedErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.expectedType.withBoundsAtScale(), typ)
			}
		})
	}
}

func TestDecimalConvert(t *testing.T) {
	ctx := sql.NewEmptyContext()
	tests := []struct {
		precision   uint8
		scale       uint8
		val         interface{}
		expectedVal interface{}
		expectedErr bool
	}{
		{1, 0, nil, nil, false},
		{1, 0, true, "1", false},
		{1, 0, false, "0", false},
		{1, 0, byte(0), "0", false},
		{1, 0, int8(3), "3", false},
		{1, 0, "-3.7e0", "-4", false},
		{1, 0, uint(4), "4", false},
		{1, 0, int16(9), "9", false},
		{1, 0, "0.00000000000000000003e20", "3", false},
		{1, 0, float64(-9.4), "-9", false},
		{1, 0, float32(9.5), "", true},
		{1, 0, int32(-10), "", true},

		{1, 1, 0, "0.0", false},
		{1, 1, .01, "0.0", false},
		{1, 1, .1, "0.1", false},
		{1, 1, ".22", "0.2", false},
		{1, 1, .55, "0.6", false},
		{1, 1, "-.7863294659345624", "-0.8", false},
		{1, 1, "2634193746329327479.32030573792e-19", "0.3", false},
		{1, 1, 1, "", true},
		{1, 1, new(big.Rat).SetInt64(2), "", true},

		{5, 0, 0, "0", false},
		{5, 0, 5000.2, "5000", false},
		{5, 0, "7742", "7742", false},
		{5, 0, new(big.Float).SetFloat64(-4723.875), "-4724", false},
		{5, 0, 99999, "99999", false},
		{5, 0, "0xf8e1", "63713", false},
		{5, 0, "0b1001110101100110", "40294", false},
		{5, 0, new(big.Rat).SetFrac64(999999, 10), "", true},
		{5, 0, 673927, "", true},

		{10, 5, 0, "0.00000", false},
		{10, 5, "99999.999994", "99999.99999", false},
		{10, 5, "5.5729136e3", "5572.91360", false},
		{10, 5, "600e-2", "6.00000", false},
		{10, 5, new(big.Rat).SetFrac64(-22, 7), "-3.14286", false},
		{10, 5, 100000, "", true},
		{10, 5, "-99999.999995", "", true},

		{65, 0, "99999999999999999999999999999999999999999999999999999999999999999",
			"99999999999999999999999999999999999999999999999999999999999999999", false},
		{65, 0, "99999999999999999999999999999999999999999999999999999999999999999.1",
			"99999999999999999999999999999999999999999999999999999999999999999", false},
		{65, 0, "99999999999999999999999999999999999999999999999999999999999999999.99", "", true},

		{65, 12, "16976349273982359874209023948672021737840592720387475.2719128737543572927374503832837350563300243035038234972093785",
			"16976349273982359874209023948672021737840592720387475.271912873754", false},
		{65, 12, "99999999999999999999999999999999999999999999999999999.9999999999999", "", true},

		{20, 10, []byte{32}, "0", false},
		{20, 10, time.Date(2019, 12, 12, 12, 12, 12, 0, time.UTC), nil, true},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%v %v %v", test.precision, test.scale, test.val), func(t *testing.T) {
			typ := MustCreateDecimalType(test.precision, test.scale)
			val, _, err := typ.Convert(ctx, test.val)
			if test.expectedErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if test.expectedVal == nil {
					assert.Nil(t, val)
				} else {
					expectedVal, err := decimal.NewFromString(test.expectedVal.(string))
					require.NoError(t, err)
					assert.True(t, expectedVal.Equal(val.(decimal.Decimal)))
					assert.Equal(t, typ.ValueType(), reflect.TypeOf(val))
				}
			}
		})
	}
}

func TestDecimalString(t *testing.T) {
	tests := []struct {
		precision   uint8
		scale       uint8
		expectedStr string
	}{
		{0, 0, "decimal(10,0)"},
		{1, 0, "decimal(1,0)"},
		{5, 0, "decimal(5,0)"},
		{10, 0, "decimal(10,0)"},
		{65, 0, "decimal(65,0)"},
		{1, 1, "decimal(1,1)"},
		{5, 1, "decimal(5,1)"},
		{10, 1, "decimal(10,1)"},
		{65, 1, "decimal(65,1)"},
		{5, 5, "decimal(5,5)"},
		{10, 5, "decimal(10,5)"},
		{65, 5, "decimal(65,5)"},
		{10, 10, "decimal(10,10)"},
		{65, 10, "decimal(65,10)"},
		{65, 30, "decimal(65,30)"},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%v %v %v", test.precision, test.scale, test.expectedStr), func(t *testing.T) {
			str := MustCreateDecimalType(test.precision, test.scale).String()
			assert.Equal(t, test.expectedStr, str)
		})
	}
}

func TestDecimalZero(t *testing.T) {
	tests := []struct {
		precision uint8
		scale     uint8
	}{
		{0, 0},
		{1, 0},
		{5, 0},
		{10, 0},
		{65, 0},
		{1, 1},
		{5, 1},
		{10, 1},
		{65, 1},
		{5, 5},
		{10, 5},
		{65, 5},
		{10, 10},
		{65, 10},
		{65, 30},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%v %v zero", test.precision, test.scale), func(t *testing.T) {
			dt := MustCreateDecimalType(test.precision, test.scale)
			_, ok := dt.Zero().(decimal.Decimal)
			assert.True(t, ok)
		})
	}
}

// boundsCheckReference is a verbatim copy of the BoundsCheck body before the at-scale fast path was added. It is the
// oracle for TestDecimalBoundsCheckFastPath and the "before" side of BenchmarkDecimalBoundsCheckReference.
func boundsCheckReference(t DecimalType_, v decimal.Decimal) (decimal.Decimal, sql.ConvertInRange, error) {
	if -v.Exponent() > int32(t.scale) {
		// TODO : add 'Data truncated' warning
		v = v.Round(int32(t.scale))
	}
	// TODO add shortcut for common case
	// ex: certain num of bits fast tracks OK
	if !v.Abs().LessThan(t.exclusiveUpperBound) {
		return decimal.Decimal{}, sql.InRange, ErrConvertToDecimalLimit.New()
	}
	return v, sql.InRange, nil
}

// TestDecimalBoundsCheckFastPath checks that BoundsCheck gives exactly the result of the pre-fast-path implementation
// (value, exponent, range flag and error) for values at the type's scale and at other exponents.
func TestDecimalBoundsCheckFastPath(t *testing.T) {
	types := []DecimalType_{
		MustCreateDecimalType(18, 2).(DecimalType_),
		MustCreateDecimalType(5, 0).(DecimalType_),
		MustCreateDecimalType(10, 10).(DecimalType_),
		MustCreateDecimalType(65, 30).(DecimalType_),
		MustCreateColumnDecimalType(18, 2).(DecimalType_),
		InternalDecimalType.(DecimalType_),
		{}, // zero value: bounds never computed, must keep the general path
	}
	for _, typ := range types {
		t.Run(fmt.Sprintf("%d_%d_%v", typ.precision, typ.scale, typ.definesColumn), func(t *testing.T) {
			exp := -int32(typ.scale)
			// bound is exclusiveUpperBound expressed at exponent -scale; ulp is one unit at that exponent.
			bound := new(big.Int).Mul(typ.exclusiveUpperBound.Coefficient(),
				new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(typ.exclusiveUpperBound.Exponent())-int64(exp)), nil))
			atScale := func(c *big.Int) decimal.Decimal { return decimal.NewFromBigInt(c, exp) }
			one := big.NewInt(1)
			values := []decimal.Decimal{
				atScale(big.NewInt(0)),
				atScale(new(big.Int).Sub(bound, one)),
				atScale(new(big.Int).Neg(new(big.Int).Sub(bound, one))),
				atScale(new(big.Int).Set(bound)),
				atScale(new(big.Int).Neg(bound)),
				atScale(new(big.Int).Add(bound, one)),
				atScale(new(big.Int).Neg(new(big.Int).Add(bound, one))),
				atScale(decimal.RequireFromString("-1234.56").Shift(int32(typ.scale)).Truncate(0).BigInt()),
				// Other exponents: the general (rounding) path must still run.
				decimal.New(-123456789, exp-1),
				decimal.New(5, exp-1),
				decimal.New(-5, exp-1),
				decimal.NewFromBigInt(new(big.Int).Sub(new(big.Int).Mul(bound, big.NewInt(10)), big.NewInt(5)), exp-1),
				decimal.New(7, 0),
				decimal.New(-7, 0),
				decimal.New(3, 2),
				decimal.New(-3, 2),
				decimal.Decimal{},
			}
			for _, v := range values {
				got, gotRange, gotErr := typ.BoundsCheck(v)
				want, wantRange, wantErr := boundsCheckReference(typ, v)
				require.Equal(t, wantRange, gotRange, "range for %s (exp %d)", v, v.Exponent())
				if wantErr != nil {
					require.True(t, ErrConvertToDecimalLimit.Is(wantErr))
					require.Error(t, gotErr, "value %s (exp %d)", v, v.Exponent())
					require.True(t, ErrConvertToDecimalLimit.Is(gotErr), "value %s (exp %d)", v, v.Exponent())
				} else {
					require.NoError(t, gotErr, "value %s (exp %d)", v, v.Exponent())
				}
				require.True(t, got.Cmp(want) == 0 && got.Exponent() == want.Exponent(),
					"value %s (exp %d): got %s (exp %d), want %s (exp %d)",
					v, v.Exponent(), got, got.Exponent(), want, want.Exponent())
			}
		})
	}
}

// TestDecimalBoundsCheckFastPathAllocs checks that BoundsCheck does not allocate for a value at the column's scale and
// reports the allocations of the general path and of Convert.
func TestDecimalBoundsCheckFastPathAllocs(t *testing.T) {
	typ := MustCreateDecimalType(18, 2).(DecimalType_)
	atScale := decimal.RequireFromString("-1234.56")
	offScale := decimal.RequireFromString("1234.5")
	ctx := context.Background()

	fast := testing.AllocsPerRun(1000, func() { _, _, _ = typ.BoundsCheck(atScale) })
	require.Equal(t, 0.0, fast, "BoundsCheck at scale must not allocate")
	slow := testing.AllocsPerRun(1000, func() { _, _, _ = typ.BoundsCheck(offScale) })
	t.Logf("BoundsCheck allocs: at scale (exp -2) = %v, off scale (exp -1) = %v", fast, slow)

	// Box the argument once so only Convert's own allocations are counted.
	var boxed interface{} = atScale
	convert := testing.AllocsPerRun(1000, func() { _, _, _ = typ.Convert(ctx, boxed) })
	t.Logf("Convert(decimal at scale) allocs = %v", convert)
	require.LessOrEqual(t, convert, 1.0, "Convert at scale should only box the result")
}

// BenchmarkDecimalConvertAtScale measures DECIMAL(18,2).Convert on a decimal already at the column's scale (fast
// path) and on a float64 input.
func BenchmarkDecimalConvertAtScale(b *testing.B) {
	typ := MustCreateDecimalType(18, 2)
	ctx := context.Background()
	b.Run("decimal", func(b *testing.B) {
		var v interface{} = decimal.RequireFromString("-1234.56")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _, _ = typ.Convert(ctx, v)
		}
	})
	b.Run("float64", func(b *testing.B) {
		var v interface{} = -1234.56
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _, _ = typ.Convert(ctx, v)
		}
	})
}

// BenchmarkDecimalBoundsCheckReference measures the pre-fast-path BoundsCheck on the value used by
// BenchmarkDecimalConvertAtScale, for a before/after comparison in one run.
func BenchmarkDecimalBoundsCheckReference(b *testing.B) {
	typ := MustCreateDecimalType(18, 2).(DecimalType_)
	v := decimal.RequireFromString("-1234.56")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _ = boundsCheckReference(typ, v)
	}
}

// convertColumnDecimalReference is a copy of the decimal.Decimal branch of ConvertToNullDecimal before the sign fix:
// it compared the exponent against +scale instead of -scale.
func convertColumnDecimalReference(t DecimalType_, v decimal.Decimal) (decimal.Decimal, error) {
	if t.definesColumn && v.Exponent() != int32(t.scale) {
		return decimal.NewFromString(v.StringFixed(int32(t.scale)))
	}
	return v, nil
}

// TestDecimalColumnConvertAtScale checks ConvertToNullDecimal on decimal inputs against the pre-fix reference: values
// are always numerically equal, exponents are equal except for exponent +scale (scale > 0), which column types now
// normalize to -scale. Non-column types must be unaffected.
func TestDecimalColumnConvertAtScale(t *testing.T) {
	types := []DecimalType_{
		MustCreateColumnDecimalType(10, 2).(DecimalType_),
		MustCreateColumnDecimalType(18, 2).(DecimalType_),
		MustCreateColumnDecimalType(5, 0).(DecimalType_),
		MustCreateColumnDecimalType(10, 10).(DecimalType_),
	}
	nonColumn := MustCreateDecimalType(18, 2).(DecimalType_)
	for _, typ := range types {
		t.Run(fmt.Sprintf("%d_%d", typ.precision, typ.scale), func(t *testing.T) {
			s := int32(typ.scale)
			exps := []int32{-s - 1, -s, 0, s, s + 1}
			if s > 0 {
				exps = append(exps, -s+1)
			}
			for _, exp := range exps {
				for _, coef := range []int64{3, -3, 0, 12345, -12345} {
					v := decimal.New(coef, exp)
					got, err := typ.ConvertToNullDecimal(v)
					require.NoError(t, err)
					require.True(t, got.Valid)
					want, err := convertColumnDecimalReference(typ, v)
					require.NoError(t, err)
					require.Equal(t, 0, got.Decimal.Cmp(want), "value %s (exp %d)", v, exp)
					if s > 0 && exp == s {
						require.Equal(t, s, want.Exponent(), "reference skips normalization at exp +scale")
						require.Equal(t, -s, got.Decimal.Exponent(), "value %s (exp %d) must be normalized", v, exp)
					} else {
						require.Equal(t, want.Exponent(), got.Decimal.Exponent(), "value %s (exp %d)", v, exp)
					}

					// A non-column type returns the value unchanged.
					nc, err := nonColumn.ConvertToNullDecimal(v)
					require.NoError(t, err)
					require.Equal(t, 0, nc.Decimal.Cmp(v))
					require.Equal(t, v.Exponent(), nc.Decimal.Exponent())
				}
			}
		})
	}

	typ := MustCreateColumnDecimalType(18, 2).(DecimalType_)
	v := decimal.RequireFromString("-1234.56")
	// Box the argument once so only ConvertToNullDecimal's own allocations are counted.
	var boxed interface{} = v
	allocs := testing.AllocsPerRun(1000, func() { _, _ = typ.ConvertToNullDecimal(boxed) })
	ref := testing.AllocsPerRun(1000, func() { _, _ = convertColumnDecimalReference(typ, v) })
	t.Logf("column DECIMAL(18,2) ConvertToNullDecimal(-1234.56) allocs: new = %v, reference = %v", allocs, ref)
	require.Equal(t, 0.0, allocs)
}

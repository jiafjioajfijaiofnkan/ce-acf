// Package ceacf 实现了 Cardinality Estimation Adaptive Cuckoo Filter (CE-ACF).
// CE-ACF 是一种概率数据结构，它结合了自适应布谷鸟过滤器的近似成员检查功能 (Approximate Membership Checking)
// 和对未存储在过滤器中元素的基数估计 (Cardinality Estimation) 能力。
//
// CE-ACF 基于 Adaptive Cuckoo Filter (ACF) 构建，特别关注论文中描述的四表 (d=4) 且
// 每个单元只有一个选择器位 (selector bit, sb=1) 的配置。
// 核心思想是利用选择器位的状态来估计查询过的负样本（未插入过滤器的元素）的基数。
//
// 主要特性:
//   - 近似成员检查：快速判断一个元素是否可能存在于集合中，允许一定的假阳性率。
//   - 自适应性：当检测到假阳性时，过滤器可以调整自身以消除该特定查询项的后续假阳性。
//   - 基数估计：能够估计在过滤器上查询过的、但未被插入的元素的唯一数量（基数）。
//   - 遵循论文中的理论模型进行基数估计。
package ceacf

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
)

const numTables = 4 // d = 4, ACF论文中推荐的表数量，本实现固定为此值。

// Bucket 代表过滤器中的一个桶（或称为单元/槽）。
// 根据CE-ACF论文的描述（四表配置，每个桶一个单元），这里的Bucket即一个单元。
type Bucket struct {
	fingerprint    uint32 // 存储元素的f位指纹。
	selectorBit    uint8  // 选择器位 (0 或 1)，用于自适应调整和选择指纹哈希函数。
	occupied       bool   // 标记该桶是否被占用。
	relocationHash uint32 // 用于Cuckoo踢出时重定位的哈希值，基于原始插入项计算。
}

// Table 代表过滤器中的一个子表，它由多个桶组成。
type Table []Bucket

// CEACF 是 Cardinality Estimation Adaptive Cuckoo Filter 的主数据结构。
// 它包含了多个表，并维护了过滤器操作所需的状态信息。
type CEACF struct {
	tables             [numTables]Table // 固定数量的子表。
	numBucketsPerTable int              // b: 每个子表的桶数量。
	fingerprintBits    uint             // f: 指纹的位数 (例如 8, 10, 12, 14, 16)。
	fingerprintMask    uint32           // 用于从哈希值中提取指纹的掩码, 等于 (1 << fingerprintBits) - 1。
	numItems           int              // 当前存储在过滤器中的元素总数。
	maxKicks           int              // Cuckoo Hashing 在插入时允许的最大踢出（位移）次数。
	tableSeeds         [numTables]uint32 // 用于各表桶索引的种子: tableSeeds[i] for table i
	fingerprintSeeds   [2]uint32         // 用于 fp0 和 fp1 的种子: fingerprintSeeds[0] for fp0, fingerprintSeeds[1] for fp1
	relocationSeed     uint32           // 用于计算 relocationHash 的独立种子。
}

// hash 使用 FNV-1a 哈希算法配合种子生成数据的64位哈希值。
// data: 要哈希的数据。
// seed: 用于改变哈希结果的种子。
// 返回一个64位哈希值。
func hash(data []byte, seed uint32) uint64 {
	h := fnv.New64a()
	// 将种子转换为字节并写入哈希，确保不同种子产生不同的哈希序列，增强哈希独立性。
	seedBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(seedBytes, seed)
	h.Write(seedBytes)
	h.Write(data)
	return h.Sum64()
}

// NewCEACF 创建并初始化一个新的 CEACF 实例。
//
// 参数:
//   - numBucketsPerTable (b): 每个子表的桶（单元）数量。此值影响过滤器的容量和哈希碰撞的概率。
//   - fingerprintBits (f): 用于每个元素指纹的位数。通常在7到16之间。较多的位数可以降低假阳性率，但会增加内存消耗。
//   - maxKicks: 在Cuckoo Hashing插入过程中，一个元素在被判断为插入失败（过滤器可能已满）之前，允许尝试踢出其他元素的最大次数。
//
// 返回:
//   - 指向初始化后的 CEACF 实例的指针。
//   - 如果参数无效或初始化过程中发生错误，则返回错误信息。
func NewCEACF(numBucketsPerTable int, fingerprintBits uint, maxKicks int) (*CEACF, error) {
	if numBucketsPerTable <= 0 {
		return nil, fmt.Errorf("每个表的桶数量 (numBucketsPerTable) 必须为正数，得到 %d", numBucketsPerTable)
	}
	if fingerprintBits < 4 || fingerprintBits > 32 {
		return nil, fmt.Errorf("指纹位数 (fingerprintBits) 必须在 [4, 32] 范围内")
	}
	if maxKicks <= 0 {
		return nil, fmt.Errorf("最大踢出次数 (maxKicks) 必须为正数")
	}

	var tableSeeds [numTables]uint32
	for i := 0; i < numTables; i++ {
		var seedBytes [4]byte
		if _, err := rand.Read(seedBytes[:]); err != nil {
			return nil, fmt.Errorf("生成表哈希种子失败: %v", err)
		}
		tableSeeds[i] = binary.LittleEndian.Uint32(seedBytes[:])
	}

	var fingerprintSeeds [2]uint32
	for i := 0; i < 2; i++ {
		var seedBytes [4]byte
		if _, err := rand.Read(seedBytes[:]); err != nil {
			return nil, fmt.Errorf("生成指纹哈希种子失败: %v", err)
		}
		fingerprintSeeds[i] = binary.LittleEndian.Uint32(seedBytes[:])
	}

	var relocSeedBytes [4]byte
	if _, err := rand.Read(relocSeedBytes[:]); err != nil {
		return nil, fmt.Errorf("生成 relocation 哈希种子失败: %v", err)
	}
	relocationSeed := binary.LittleEndian.Uint32(relocSeedBytes[:])

	filter := &CEACF{
		numBucketsPerTable: numBucketsPerTable,
		fingerprintBits:    fingerprintBits,
		fingerprintMask:    (1 << fingerprintBits) - 1,
		maxKicks:           maxKicks,
		tableSeeds:         tableSeeds,
		fingerprintSeeds:   fingerprintSeeds,
		relocationSeed:     relocationSeed,
	}

	for i := 0; i < numTables; i++ {
		filter.tables[i] = make(Table, numBucketsPerTable)
	}

	return filter, nil
}

// getFingerprint 计算给定数据和选择器位的指纹。
//
// 实现细节:
//   - 使用预定义的种子 cf.fingerprintSeeds[0] 生成指纹fp0(data)。
//   - 使用预定义的种子 cf.fingerprintSeeds[1] 生成指纹fp1(data)。
//   - 指纹值不能为0，如果计算结果为0，则替换为1（或其他非零固定值）。
func (cf *CEACF) getFingerprint(data []byte, tableIndex int, selectorBit uint8) uint32 {
	var seedForFingerprint uint32
	if selectorBit == 0 {
		seedForFingerprint = cf.fingerprintSeeds[0]
	} else {
		seedForFingerprint = cf.fingerprintSeeds[1]
	}
	fp := uint32(hash(data, seedForFingerprint)) & cf.fingerprintMask
	if fp == 0 {
		fp = 1
	}
	return fp
}

// getBucketIndex 计算给定数据在指定子表中的桶索引。
// 每个子表使用不同的哈希种子 (cf.tableSeeds[tableIndex]) 来计算桶索引，
// 以确保元素映射到不同子表的不同位置。
//
// 参数:
//   - data: 要计算桶索引的元素数据。
//   - tableIndex: 目标子表的索引 (0 到 numTables-1)。
//
// 返回:
//   - 计算得到的桶索引。
func (cf *CEACF) getBucketIndex(data []byte, tableIndex int) uint32 {
	return uint32(hash(data, cf.tableSeeds[tableIndex]) % uint64(cf.numBucketsPerTable))
}

// getRelocationHash 计算给定数据的重定位哈希。
func (cf *CEACF) getRelocationHash(data []byte) uint32 {
	return uint32(hash(data, cf.relocationSeed))
}


// Occupancy 返回过滤器的当前占用率。
// 占用率定义为已存储元素的数量与过滤器总桶数（所有子表中的桶总和）的比率。
// 返回值范围在 [0.0, 1.0] 之间。
func (cf *CEACF) Occupancy() float64 {
	if cf.numBucketsPerTable == 0 {
		return 0
	}
	totalBuckets := numTables * cf.numBucketsPerTable
	if totalBuckets == 0 {
		return 0
	}
	return float64(cf.numItems) / float64(totalBuckets)
}

// Insert 将一个元素插入到 CEACF 过滤器中。
func (cf *CEACF) Insert(item []byte) bool {
	fp0 := cf.getFingerprint(item, 0, 0)
	itemRelocationHash := cf.getRelocationHash(item)

	candidateIndices := [numTables]uint32{}
	for i := 0; i < numTables; i++ {
		candidateIndices[i] = cf.getBucketIndex(item, i)
	}

	for i := 0; i < numTables; i++ {
		table := &cf.tables[i]
		idx := candidateIndices[i]
		if !(*table)[idx].occupied {
			(*table)[idx].fingerprint = fp0
			(*table)[idx].selectorBit = 0
			(*table)[idx].occupied = true
			(*table)[idx].relocationHash = itemRelocationHash
			cf.numItems++
			return true
		}
	}

	// 如果所有直接候选位置都满了，则开始Cuckoo Hashing踢出流程
	currentFp := fp0
	currentSelector := uint8(0)
	currentRelocHash := itemRelocationHash

	// 起始踢出表索引是基于原始 item 的哈希，从其候选位置之一开始
	currentKickTableIdx := int(hash(item, cf.fingerprintSeeds[1]) % numTables) // 使用指纹种子（或其他独立种子）来选择起始表

	for kick := 0; kick < cf.maxKicks; kick++ {
		var bucketToKickIdx uint32
		if kick == 0 {
			// 第一次踢出，使用原始item在其选择的currentKickTableIdx中的候选索引
			bucketToKickIdx = candidateIndices[currentKickTableIdx]
		} else {
			// 后续踢出，currentRelocHash 是上一个受害者的relocHash
			// 我们需要基于 currentRelocHash 找到它在 currentKickTableIdx 中的位置
			bucketToKickIdx = cf.getBucketIndexFromRelocHash(currentRelocHash, currentKickTableIdx)
		}

		// 保存被踢出的元素信息
		victimFp := cf.tables[currentKickTableIdx][bucketToKickIdx].fingerprint
		victimSelector := cf.tables[currentKickTableIdx][bucketToKickIdx].selectorBit
		victimRelocHash := cf.tables[currentKickTableIdx][bucketToKickIdx].relocationHash

		// 将当前要插入的元素(currentFp, currentSelector, currentRelocHash)放入该桶
		cf.tables[currentKickTableIdx][bucketToKickIdx].fingerprint = currentFp
		cf.tables[currentKickTableIdx][bucketToKickIdx].selectorBit = currentSelector
		cf.tables[currentKickTableIdx][bucketToKickIdx].relocationHash = currentRelocHash

		// 更新当前要插入的元素为被踢出的元素 (受害者)
		currentFp = victimFp
		currentSelector = victimSelector
		currentRelocHash = victimRelocHash

		// 为受害者 (新的currentFp, currentSelector, currentRelocHash) 寻找新的位置
		// 计算受害者在其所有表中的“规范”位置，使用其自身的 relocationHash
		victimCandidateIndices := [numTables]uint32{}
		for tableJ := 0; tableJ < numTables; tableJ++ {
			victimCandidateIndices[tableJ] = cf.getBucketIndexFromRelocHash(currentRelocHash, tableJ)
		}

		// 尝试将受害者放入其其他 numTables-1 个候选位置中的一个空桶
		// 它刚从 currentKickTableIdx 被踢出
		for i := 1; i < numTables; i++ {
			nextTableToTry := (currentKickTableIdx + i) % numTables
			nextBucketToTry := victimCandidateIndices[nextTableToTry]

			if !cf.tables[nextTableToTry][nextBucketToTry].occupied {
				cf.tables[nextTableToTry][nextBucketToTry].fingerprint = currentFp
				cf.tables[nextTableToTry][nextBucketToTry].selectorBit = currentSelector
				cf.tables[nextTableToTry][nextBucketToTry].occupied = true
				cf.tables[nextTableToTry][nextBucketToTry].relocationHash = currentRelocHash
				return true // 原始item的插入路径已解决
			}
		}

		// 如果所有备用位置都满了，为下一次踢出选择一个新的表。
		// 这个选择是基于当前受害者 (currentRelocHash) 的。
		// 避免选择它刚刚被踢出的那个表 (currentKickTableIdx)。
		// 使用 relocationHash 和 kick 次数来增加随机性
		relocBytesForNextKick := make([]byte, 4)
		binary.LittleEndian.PutUint32(relocBytesForNextKick, currentRelocHash)
		nextKickTableAttempt := int(hash(relocBytesForNextKick, uint32(kick)) % (numTables -1)) // Produces 0 to numTables-2
		currentKickTableIdx = (currentKickTableIdx + 1 + nextKickTableAttempt) % numTables
	}
	return false
}

// getBucketIndexFromRelocHash 是一个辅助函数，用于根据relocationHash计算桶索引
func (cf *CEACF) getBucketIndexFromRelocHash(relocHash uint32, tableIndex int) uint32 {
	relocBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(relocBytes, relocHash)
	// 使用与getBucketIndex相同的表种子进行哈希
	return uint32(hash(relocBytes, cf.tableSeeds[tableIndex]) % uint64(cf.numBucketsPerTable))
}

// Lookup 检查一个元素是否（可能）存在于过滤器中。
func (cf *CEACF) Lookup(item []byte) bool {
	indices := [numTables]uint32{}
	for i := 0; i < numTables; i++ {
		indices[i] = cf.getBucketIndex(item, i)
	}

	for i := 0; i < numTables; i++ {
		table := cf.tables[i]
		idx := indices[i]
		bucket := table[idx]

		if bucket.occupied {
			selectorBitInBucket := bucket.selectorBit
			fpToCompare := cf.getFingerprint(item, i, selectorBitInBucket)

			if bucket.fingerprint == fpToCompare {
				return true
			}
		}
	}
	return false
}

// Delete 从过滤器中删除一个元素（如果存在）。
func (cf *CEACF) Delete(item []byte) bool {
	indices := [numTables]uint32{}
	for i := 0; i < numTables; i++ {
		indices[i] = cf.getBucketIndex(item, i)
	}

	itemRelocHash := cf.getRelocationHash(item)

	for i := 0; i < numTables; i++ {
		table := &cf.tables[i]
		idx := indices[i]
		bucket := &(*table)[idx]

		if bucket.occupied {
			selectorBitInBucket := bucket.selectorBit
			fpToCompare := cf.getFingerprint(item, i, selectorBitInBucket)

			if bucket.fingerprint == fpToCompare && bucket.relocationHash == itemRelocHash {
				bucket.occupied = false
				cf.numItems--
				return true
			}
		}
	}
	return false
}

// Adapt 在检测到假阳性 (False Positive, FP) 时调整过滤器内容
func (cf *CEACF) Adapt(queriedItem []byte, actualKeyInTable []byte) bool {
	indicesActualKey := [numTables]uint32{}
	for i := 0; i < numTables; i++ {
		indicesActualKey[i] = cf.getBucketIndex(actualKeyInTable, i)
	}

	actualKeyRelocHash := cf.getRelocationHash(actualKeyInTable)

	foundLocation := false
	tableIdxOfActualKey := -1
	bucketIdxOfActualKey := uint32(0)

	for i := 0; i < numTables; i++ {
		table := cf.tables[i]
		idx := indicesActualKey[i]
		bucket := table[idx]

		if bucket.occupied {
			fpActual := cf.getFingerprint(actualKeyInTable, i, bucket.selectorBit)
			if bucket.fingerprint == fpActual && bucket.relocationHash == actualKeyRelocHash {
				fpQueried := cf.getFingerprint(queriedItem, i, bucket.selectorBit)
				if bucket.fingerprint == fpQueried && string(queriedItem) != string(actualKeyInTable) {
					tableIdxOfActualKey = i
					bucketIdxOfActualKey = idx
					foundLocation = true
					break
				}
			}
		}
	}

	if !foundLocation {
		return false
	}

	currentSelectorBit := cf.tables[tableIdxOfActualKey][bucketIdxOfActualKey].selectorBit
	newSelectorBit := uint8(1) - currentSelectorBit

	newFingerprint := cf.getFingerprint(actualKeyInTable, tableIdxOfActualKey, newSelectorBit)

	cf.tables[tableIdxOfActualKey][bucketIdxOfActualKey].selectorBit = newSelectorBit
	cf.tables[tableIdxOfActualKey][bucketIdxOfActualKey].fingerprint = newFingerprint
	return true
}

// EstimateCardinality 估计在过滤器上查询过的、但未插入其中的负样本的基数
func (cf *CEACF) EstimateCardinality() (float64, error) {
	if cf.numItems == 0 {
		return 0, nil
	}

	numOccupiedWithS1 := 0
	for i := 0; i < numTables; i++ {
		for j := 0; j < cf.numBucketsPerTable; j++ {
			if cf.tables[i][j].occupied {
				if cf.tables[i][j].selectorBit == 1 {
					numOccupiedWithS1++
				}
			}
		}
	}

	if cf.numItems == 0 {
		return 0, nil
	}

	pHat1 := float64(numOccupiedWithS1) / float64(cf.numItems)

	if pHat1 >= 0.5 {
		return 0, fmt.Errorf("pHat1 (%.4f) 大于或等于 0.5，基数估计无效。可能基数过大", pHat1)
	}
	if pHat1 < 0 {
		return 0, fmt.Errorf("pHat1 (%.4f) 小于 0，内部错误", pHat1)
	}

	termB := float64(cf.numBucketsPerTable)
	term2PowFminus1 := math.Pow(2, float64(cf.fingerprintBits-1))
	termLn := math.Log(1 - 2*pHat1)

	if math.IsNaN(termLn) || math.IsInf(termLn, 0) {
		return 0, fmt.Errorf("ln(1 - 2*pHat1) 计算结果无效 (pHat1=%.4f)", pHat1)
	}

	estimatedC := -termB * term2PowFminus1 * termLn
	return estimatedC, nil
}

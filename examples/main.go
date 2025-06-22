package main

import (
	"ceacf" // 导入本地模块
	"fmt"
	"math/rand"
)

func main() {
	fmt.Println("CE-ACF 示例")
	fmt.Println("----------------")

	// 1. 创建 CEACF 实例
	// 参数: 每个表的桶数量 (b), 指纹位数 (f), 最大踢出次数
	// 根据论文，b通常较大 (如1024, 4096, 65536), f在7-16之间
	filter, err := ceacf.NewCEACF(1024, 10, 10)
	if err != nil {
		fmt.Printf("创建过滤器失败: %v\n", err)
		return
	}
	fmt.Println("过滤器创建成功。")
	fmt.Printf("参数: 每个表桶数=%d, 指纹位数=%d, 表数量=%d\n", 1024, 10, 4) // numTables is const 4

	// 2. 插入元素
	itemsToInsert := [][]byte{
		[]byte("apple"),
		[]byte("banana"),
		[]byte("cherry"),
		[]byte("coconut"),
		[]byte("grape"),
	}
	fmt.Println("\n插入元素:")
	for _, item := range itemsToInsert {
		if filter.Insert(item) {
			fmt.Printf("  成功插入: %s\n", string(item))
		} else {
			fmt.Printf("  插入失败 (过滤器可能已满或踢出次数耗尽): %s\n", string(item))
		}
	}
	fmt.Printf("  当前占用率: %.2f%%\n", filter.Occupancy()*100)

	// 3. 查询元素
	fmt.Println("\n查询元素:")
	itemsToLookup := [][]byte{
		[]byte("apple"),       // 存在
		[]byte("banana"),      // 存在
		[]byte("date"),        // 不存在
		[]byte("elderberry"),  // 不存在 (潜在的假阳性)
		[]byte("grape"),       // 存在
		[]byte("watermelon"),  // 不存在
	}

	// 模拟一个外部存储或验证机制，用于确认假阳性
	// 在这个示例中，我们知道哪些是真的插入了
	groundTruth := make(map[string]bool)
	for _, item := range itemsToInsert {
		groundTruth[string(item)] = true
	}

	// 存储可能导致假阳性的查询项和实际存储项的对应关系
	// (queriedItem, actualItemInFilterThatCausedFP)
	potentialAdaptations := [][2][]byte{}

	for _, item := range itemsToLookup {
		if filter.Lookup(item) {
			fmt.Printf("  查询到元素 '%s' (可能为真，也可能为假阳性)\n", string(item))
			if !groundTruth[string(item)] {
				fmt.Printf("    !! 检测到对 '%s' 的潜在假阳性 (因为它实际未插入)\n", string(item))
				// 在真实场景中，我们需要找到是哪个过滤器中的元素导致了这个假阳性。
				// CE-ACF的Adapt需要知道 (导致FP的查询项, 过滤器中实际存储的项)
				// 这里简化：如果查询项是 "elderberry"，我们假设它与 "apple" 发生了碰撞
				// (这需要运气或者更复杂的查找逻辑来确定实际碰撞的项)
				// 为了演示，我们随机选择一个已插入项作为“实际存储项”
				// 注意：这只是为了演示Adapt的调用，实际应用中需要准确找到碰撞的键
				if len(itemsToInsert) > 0 {
					actualItem := itemsToInsert[rand.Intn(len(itemsToInsert))]
					// 仅当此假阳性确实是由actualItem引起时，Adapt才有效
					// Adapt内部会验证这一点。
					fmt.Printf("      (为了演示，假设它与 '%s' 碰撞)\n", string(actualItem))
					potentialAdaptations = append(potentialAdaptations, [2][]byte{item, actualItem})
				}
			}
		} else {
			fmt.Printf("  未查询到元素: %s\n", string(item))
		}
	}

	// 4. 执行自适应 (基于上面收集到的潜在假阳性)
	if len(potentialAdaptations) > 0 {
		fmt.Println("\n执行自适应操作:")
		for _, pair := range potentialAdaptations {
			queried := pair[0]
			actual := pair[1]
			fmt.Printf("  尝试为查询 '%s' (假定与 '%s' 碰撞) 进行自适应...\n", string(queried), string(actual))
			if filter.Adapt(queried, actual) {
				fmt.Printf("    自适应成功。再次查询 '%s': ", string(queried))
				if filter.Lookup(queried) {
					fmt.Println("仍然存在 (可能碰撞到其他项或自适应未完全消除此特定FP路径)")
				} else {
					fmt.Println("不存在 (自适应可能已消除此FP)")
				}
			} else {
				fmt.Printf("    自适应失败 (可能 '%s' 与 '%s' 在过滤器中的指纹/位置不匹配，或 '%s' 不在过滤器中)\n", string(queried), string(actual), string(actual))
			}
		}
	}


	// 5. 模拟负样本查询并估计基数
	// 论文的基数估计是针对在过滤器上查询的“负样本”（即未插入过滤器的元素）
	// 它们的查询（以及可能引发的Adapt）会改变过滤器中选择器位的分布
	fmt.Println("\n模拟负样本查询以影响选择器位并估计基数:")
	negativeSamples := [][]byte{
		[]byte("fig"), []byte("grapefruit"), []byte("honeydew"), []byte("kiwi"),
		[]byte("lemon"), []byte("lime"), []byte("mango"), []byte("nectarine"),
		[]byte("orange"), []byte("papaya"), []byte("peach"), []byte("pear"),
		// 重复一些来模拟 skewed query distributions
		[]byte("fig"), []byte("lemon"), []byte("orange"), []byte("fig"),
	}
	// 实际不重复的负样本数
	distinctNegativeSamples := make(map[string]struct{})
	for _, ns := range negativeSamples {
		distinctNegativeSamples[string(ns)] = struct{}{}
	}


	fmt.Printf("  将对 %d 个负样本 (其中 %d 个是唯一的) 进行查询和可能的自适应:\n", len(negativeSamples), len(distinctNegativeSamples))
	for _, negItem := range negativeSamples {
		// 模拟查询过程：如果查询导致Lookup为true（即一个假阳性）
		// 并且我们能从外部源（如主哈希表）知道这是个假阳性，
		// 并且知道是哪个过滤器内的元素 `actualInFilter` 导致了这个假阳性，
		// 那么就调用 Adapt(negItem, actualInFilter)。
		if filter.Lookup(negItem) {
			// 这是一个假阳性，因为 negItem 是负样本
			// 找到一个“导致”这个假阳性的真实存在的项 (随机选取一个用于演示)
			if len(itemsToInsert) > 0 {
				actualKey := itemsToInsert[rand.Intn(len(itemsToInsert))]
				// fmt.Printf("    负样本 '%s' 产生假阳性，尝试与 '%s' 进行自适应。\n", string(negItem), string(actualKey))
				filter.Adapt(negItem, actualKey) // Adapt内部会验证是否真的匹配
			}
		}
	}
	fmt.Println("  负样本查询和自适应模拟完成。")

	estimatedCardinality, err := filter.EstimateCardinality()
	if err != nil {
		fmt.Printf("  估计基数失败: %v\n", err)
	} else {
		fmt.Printf("  估计的负样本基数: %.2f (实际唯一负样本查询数: %d)\n", estimatedCardinality, len(distinctNegativeSamples))
		// 注意：估计的基数与实际负样本数可能由于多种因素（如哈希碰撞、参数选择、样本数量）而不完全一致。
		// 论文中的图表显示了其准确性范围。
	}

	// 6. 删除元素
	fmt.Println("\n删除元素:")
	itemToDelete := []byte("banana") // 之前已插入
	fmt.Printf("  尝试删除: %s\n", string(itemToDelete))
	if filter.Delete(itemToDelete) {
		fmt.Printf("    成功删除: %s\n", string(itemToDelete))
	} else {
		fmt.Printf("    删除失败 (元素 '%s' 可能不存在于过滤器中)\n", string(itemToDelete))
	}
	fmt.Printf("    删除后再次查询 '%s': ", string(itemToDelete))
	if filter.Lookup(itemToDelete) {
		fmt.Println("仍然存在 (异常或之前删除失败)")
	} else {
		fmt.Println("不存在 (符合预期)")
	}
	fmt.Printf("  当前占用率: %.2f%%\n", filter.Occupancy()*100)

	itemNotInserted := []byte("pineapple")
	fmt.Printf("  尝试删除一个未插入的元素: %s\n", string(itemNotInserted))
	if filter.Delete(itemNotInserted) {
		fmt.Printf("    成功删除: %s (异常，因为未插入过)\n", string(itemNotInserted))
	} else {
		fmt.Printf("    删除失败 (元素 '%s' 不存在，符合预期)\n", string(itemNotInserted))
	}

	fmt.Println("\n----------------")
	fmt.Println("示例结束。")
}

# Home menu icons

Cherry's own pictures for the LINE PLAY home MENU grid, served at `/cherry/menu/<file>` and
named by `icons` in `data/menu.json`. They are not game assets.

- Source: `@tabler/icons` 3.49.0 (npm), outline style, stroke 2, from `icons/outline/`.
- Licence: MIT, Copyright (c) 2020-2026 Paweł Kuna; the text is in `LICENSE-tabler-icons.txt`.
- Rendering: `scripts/render-menu-icons.py` makes each file a 128x108 RGBA PNG: a coloured circle
  badge per category and a white glyph (the client placeholder is 128x108).

| File | Glyph | Badge | Link types |
| --- | --- | --- | --- |
| `ad.png` | `ad` | `#8393A7` | `goSomewhere(homeAd)` |
| `armchair-2.png` | `armchair-2` | `#FF7FA8` | `goSomewhere(interiorShop)` |
| `arrows-exchange.png` | `arrows-exchange` | `#FF9F43` | `goSomewhere(ItemTrade)` |
| `backpack.png` | `backpack` | `#6CC650` | `goSomewhere(bag)` |
| `barrel.png` | `barrel` | `#6D7CFF` | `goSomewhere(treasureWorld)` |
| `bell.png` | `bell` | `#8393A7` | `goSomewhere(notice)` |
| `book-2.png` | `book-2` | `#F2545B` | `goSomewhere(storygacha)`, `goSomewhere(storygachaList)`, `goSomewhere(storygachaDetail)` |
| `book.png` | `book` | `#6D7CFF` | `goSomewhere(ComicGuide)`, `goSomewhere(MoreComicGuide)` |
| `bookmark.png` | `bookmark` | `#B98B65` | `goSomewhere(minipetBook)` |
| `books.png` | `books` | `#FF9F43` | `goSomewhere(collectionmain)`, `goSomewhere(collectiondetail)` |
| `box.png` | `box` | `#FF9F43` | `goSomewhere(storage)` |
| `brush.png` | `brush` | `#FF7FA8` | `goSomewhere(roomEdit)` |
| `building-cottage.png` | `building-cottage` | `#FF7FA8` | `goSomewhere(modelhouse)` |
| `building-factory-2.png` | `building-factory-2` | `#6CC650` | `goSomewhere(factory)` |
| `building-store.png` | `building-store` | `#FF9F43` | `goSomewhere(storeList)`, `goSomewhere(storeDetail)` |
| `bulb.png` | `bulb` | `#8393A7` | `goSomewhere(cherrytip)` |
| `calendar-check.png` | `calendar-check` | `#E9B824` | `goSomewhere(attendance)` |
| `calendar-event.png` | `calendar-event` | `#6D7CFF` | `event`, `goSomewhere(SquareCollaboEvent)` |
| `checklist.png` | `checklist` | `#A77BFF` | `goSomewhere(fashionQuest)`, `goSomewhere(circleFashionQuest)` |
| `circles.png` | `circles` | `#4DB8FF` | `goSomewhere(circle)` |
| `clipboard-text.png` | `clipboard-text` | `#6D7CFF` | `goSomewhere(survey)` |
| `cloud-upload.png` | `cloud-upload` | `#6CC650` | `goSomewhere(gardeneditcloud)` |
| `clover.png` | `clover` | `#6D7CFF` | `goSomewhere(eventLottoDetail)` |
| `code.png` | `code` | `#6D7CFF` | `html` |
| `coins.png` | `coins` | `#E9B824` | `goSomewhere(goldenChance)` |
| `confetti.png` | `confetti` | `#FF7FA8` | `goSomewhere(roompartyInvite)` |
| `credit-card.png` | `credit-card` | `#FF9F43` | `goSomewhere(cashShop)` |
| `crown.png` | `crown` | `#E9B824` | `goSomewhere(viplounge)`, `goSomewhere(vipenterpopup)`, `goSomewhere(vipdetail)` |
| `device-gamepad-2.png` | `device-gamepad-2` | `#6D7CFF` | `gamelink`, `gamelink(custom)`, `gamelink(embedded)` |
| `device-gamepad.png` | `device-gamepad` | `#6D7CFF` | `goSomewhere(lineGameEvent)` |
| `diamond.png` | `diamond` | `#FF9F43` | `goSomewhere(gemShop)`, `goSomewhere(rcvFreeGem)` |
| `dice-4.png` | `dice-4` | `#6CC650` | `goSomewhere(randomGarden)` |
| `dice-5.png` | `dice-5` | `#FF7FA8` | `goSomewhere(randomRoom)` |
| `dice.png` | `dice` | `#2BBFA4` | `goSomewhere(marbleboardWebview)` |
| `egg.png` | `egg` | `#B98B65` | `goSomewhere(ridingpetGacha)` |
| `fish.png` | `fish` | `#2BBFA4` | `goSomewhere(fishingExchange)` |
| `flag-3.png` | `flag-3` | `#2BBFA4` | `goSomewhere(ridingpetplayinfo)` |
| `gift.png` | `gift` | `#F2545B` | `goSomewhere(gachaShop)`, `goSomewhere(gachaCatg)`, `goSomewhere(gachaCatgNotap)`, `goSomewhere(gachaDetail)`, `goSomewhere(gachaTop)`, `goSomewhere(gachaNew)`, `goSomewhere(gachaMain)`, `goSomewhere(gachaSearch)`, `goSomewhere(gachaKeyword)` |
| `hammer.png` | `hammer` | `#FF9F43` | `goSomewhere(remodelingShop)` |
| `hanger.png` | `hanger` | `#A77BFF` | `goSomewhere(fashionShop)` |
| `heart.png` | `heart` | `#FF9F43` | `goSomewhere(extraHeartShop)` |
| `home.png` | `home` | `#FF7FA8` | `goSomewhere(myHome)`, `goSomewhere(myRoom)`, `goSomewhere(otherRoom)` |
| `hourglass.png` | `hourglass` | `#6D7CFF` | `goSomewhere(timemagic)` |
| `layout-grid.png` | `layout-grid` | `#8393A7` | `goSomewhere(subMoreShop)`, `goSomewhere(subMoreGacha)`, `goSomewhere(subMoreEvent)`, `goSomewhere(subMoreGame)` |
| `list-check.png` | `list-check` | `#E9B824` | `goSomewhere(quest)` |
| `mail-forward.png` | `mail-forward` | `#4DB8FF` | `goSomewhere(invitation)` |
| `mailbox.png` | `mailbox` | `#4DB8FF` | `goSomewhere(postbox)`, `goSomewhere(mailbox)` |
| `map-2.png` | `map-2` | `#2BBFA4` | `goSomewhere(square)`, `goSomewhere(enterSquare)` |
| `medal.png` | `medal` | `#A77BFF` | `goSomewhere(badgeShop)` |
| `messages.png` | `messages` | `#4DB8FF` | `goSomewhere(loungeChat)`, `goSomewhere(avatarChat)` |
| `mood-smile.png` | `mood-smile` | `#A77BFF` | `goSomewhere(faceShop)` |
| `notebook.png` | `notebook` | `#4DB8FF` | `goSomewhere(diary)`, `goSomewhere(diaryDetail)` |
| `package.png` | `package` | `#E9B824` | `goSomewhere(billPackageDay1)`, `goSomewhere(billPackageDay30)`, `goSomewhere(billPackage)`, `goSomewhere(billPackageItemHigh)`, `goSomewhere(billPackageItemLow)`, `goSomewhere(billPkg)` |
| `palette.png` | `palette` | `#A77BFF` | `goSomewhere(colorantShop)`, `goSomewhere(colorantBaseShop)`, `goSomewhere(colorantInven)` |
| `paw.png` | `paw` | `#B98B65` | `goSomewhere(minipetShop)` |
| `pencil.png` | `pencil` | `#6CC650` | `goSomewhere(gardenedit)` |
| `photo.png` | `photo` | `#FF7FA8` | `goSomewhere(imagePopupDetail)` |
| `plant-2.png` | `plant-2` | `#6CC650` | `goSomewhere(myGarden)` |
| `player-play.png` | `player-play` | `#6D7CFF` | `goSomewhere(videoplay)` |
| `rotate-clockwise-2.png` | `rotate-clockwise-2` | `#F2545B` | `goSomewhere(luckyspin)`, `goSomewhere(jackpotspin)` |
| `settings.png` | `settings` | `#8393A7` | `goSomewhere(setting)` |
| `shirt.png` | `shirt` | `#A77BFF` | `goSomewhere(closet)` |
| `shopping-bag.png` | `shopping-bag` | `#FF9F43` | `goSomewhere(giftShop)`, `goSomewhere(giftShopDetail)` |
| `sparkles.png` | `sparkles` | `#6CC650` | `goSomewhere(gardenbuffInfo)` |
| `star.png` | `star` | `#A77BFF` | `goSomewhere(fashionista)`, `goSomewhere(fashionistaList)` |
| `tag.png` | `tag` | `#FF9F43` | `goSomewhere(brandShop)`, `goSomewhere(brandShopEx)` |
| `thumb-up.png` | `thumb-up` | `#4DB8FF` | `goSomewhere(recommendPostList)` |
| `ticket.png` | `ticket` | `#E9B824` | `goSomewhere(playpasspopup)`, `goSomewhere(seasonPass)` |
| `trees.png` | `trees` | `#6CC650` | `goSomewhere(otherGarden)` |
| `trophy.png` | `trophy` | `#2BBFA4` | `goSomewhere(eventRanking)`, `goSomewhere(fishingBossRanking)`, `goSomewhere(minigameRanking)` |
| `user-edit.png` | `user-edit` | `#4DB8FF` | `goSomewhere(profileedit)` |
| `user-plus.png` | `user-plus` | `#4DB8FF` | `goSomewhere(addfriends)` |
| `user-star.png` | `user-star` | `#4DB8FF` | `goSomewhere(butlerShop)`, `goSomewhere(butlerBook)`, `goSomewhere(butlerSkinSelect)` |
| `users.png` | `users` | `#4DB8FF` | `goSomewhere(myfriends)`, `goSomewhere(todayMember)`, `goSomewhere(gotoFriend)` |
| `wand.png` | `wand` | `#6D7CFF` | `goSomewhere(findMagic)` |
| `world.png` | `world` | `#6D7CFF` | `openWithBrowser`, `goSomewhere(squarewebview)` |

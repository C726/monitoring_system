# 
1.ExitErrorMap这个map要保留，cmd.go里面会捕获18、28、97然后往里面写  
2.看配置文件confg.yaml里面有watchTradeID，这个是和上游配套的，最理想的状态是有几个watchTradeID就启动几个线程来做检测流程

# 功能逻辑点

1.先扫描map里面是否存在需要检测的ID，
    如果有
        则调用socks5检测和curl检测来完成，如果发现socks5检测失败，这个可以不管，直接pass，
        然后调用curl下载，如果出现下载失败的情况，执行IP切换功能，
        如果单次下载速率小于3mbps，执行切换IP的功能
    然后配置文件用有checkerr_test_num，可以配置下载次数，如果下载次数的平均速率低于3mbps，那么就判断这个节点不可用
    然后去检查这个城市ID是否存在于bad_line表中，如果不存在就写进去，如果存在则pass

2.检查good_line表，如果检查map和bad_line里面的数据为空（或者在30分钟都检测过），就从good_line表里面取一个城市ID出来  
    你看下其他的方法，里面有写取到城市ID该如何操作
    第一步更换watchTradeID的地址为目标城市，
    然后通过接口去取回来watchTradeID的连接信息
    然后传参，逻辑上检测good_line应该判断下载速率不能低于10mbps，且下载失败成功率不能超过1次
    如果发现不符合判定的话，则从good_line表里面把这个id删除。

3.map检测过了以后，需要从map中删除对应的城市id信息。